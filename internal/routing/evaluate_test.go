// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

func (s *fakeStore) GetRoutingStamp(_ context.Context, org int64) (dbgen.GetRoutingStampRow, error) {
	if err := s.call("GetRoutingStamp"); err != nil || org != orgID {
		return dbgen.GetRoutingStampRow{}, err
	}
	var versions int64
	for _, r := range s.live() {
		versions += r.Version
	}
	return dbgen.GetRoutingStampRow{RouteOrderVersion: s.order, RouteVersions: versions,
		SeverityLabel: s.severityLabel, SeverityMapping: s.mapping}, nil
}

func (s *fakeStore) ListAlertLabels(_ context.Context, arg dbgen.ListAlertLabelsParams) ([]dbgen.ListAlertLabelsRow,
	error) {
	if err := s.call("ListAlertLabels"); err != nil {
		return nil, err
	}
	var out []dbgen.ListAlertLabelsRow
	for _, id := range arg.Ids {
		if labels, ok := s.alerts[id]; ok && arg.OrgID == orgID {
			out = append(out, dbgen.ListAlertLabelsRow{ID: id, Labels: labels})
		}
	}
	return out, nil
}

func (s *fakeStore) SetAlertRoutes(_ context.Context, arg dbgen.SetAlertRoutesParams) error {
	if err := s.call("SetAlertRoutes"); err != nil {
		return err
	}
	for i, id := range arg.Ids {
		s.routed[id] = dbgen.SetAlertRoutesParams{OrgID: arg.OrgID, Ids: []int64{id},
			RouteIds: []int64{arg.RouteIds[i]}, SeverityLevels: []string{arg.SeverityLevels[i]},
			SeverityRaws: []string{arg.SeverityRaws[i]}}
	}
	return nil
}

func newRouter(store *fakeStore) *Router {
	r := NewRouter(orgID)
	r.queries = func(dbgen.DBTX) evalQueries { return store }
	return r
}

func fired(ids ...int64) []ingest.AlertChange {
	out := make([]ingest.AlertChange, len(ids))
	for i, id := range ids {
		out[i] = ingest.AlertChange{Kind: ingest.ChangeFired, AlertID: id}
	}
	return out
}

// routeOfAlert is the name of the Route that took the Alert id, empty when none did.
func routeOfAlert(t *testing.T, svc *Service, store *fakeStore, id int64) string {
	t.Helper()
	got, ok := store.routed[id]
	if !ok {
		return ""
	}
	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Routes {
		if r.ID == got.RouteIds[0] {
			return r.Name
		}
	}
	return "?"
}

// TestMatch is C-08.FR-2: the operators of Alertmanager, =~ and !~ anchored at both ends, a missing label counting
// as the empty value, and the Matchers of a Route combined with AND.
func TestMatch(t *testing.T) {
	labels := map[string]string{"severity": "critical", "pod": "api-1", "team": ""}
	for _, c := range []struct {
		ms   []Matcher
		want bool
	}{
		{nil, true},
		{[]Matcher{{"severity", "=", "critical"}}, true},
		{[]Matcher{{"severity", "=", "Critical"}}, false},
		{[]Matcher{{"severity", "!=", "critical"}}, false},
		{[]Matcher{{"severity", "!=", "warning"}}, true},
		{[]Matcher{{"severity", "=~", "crit"}}, false},
		{[]Matcher{{"severity", "=~", "crit.*"}}, true},
		{[]Matcher{{"severity", "=~", "warning|critical"}}, true},
		{[]Matcher{{"severity", "=~", "^critical$"}}, true},
		{[]Matcher{{"severity", "!~", "crit"}}, true},
		{[]Matcher{{"severity", "!~", "crit.*"}}, false},
		{[]Matcher{{"pod", "=~", "api-.*"}}, true},
		{[]Matcher{{"pod", "=~", "pi-1"}}, false},
		{[]Matcher{{"missing", "=", ""}}, true},
		{[]Matcher{{"missing", "!=", "x"}}, true},
		{[]Matcher{{"missing", "!=", ""}}, false},
		{[]Matcher{{"missing", "=~", ".*"}}, true},
		{[]Matcher{{"missing", "=~", ".+"}}, false},
		{[]Matcher{{"missing", "!~", ".+"}}, true},
		{[]Matcher{{"team", "=", ""}}, true},
		{[]Matcher{{"severity", "=", "critical"}, {"pod", "=~", "api-.*"}}, true},
		{[]Matcher{{"severity", "=", "critical"}, {"pod", "=~", "db-.*"}}, false},
	} {
		compiledMs := make([]matchers.Matcher, len(c.ms))
		for i, m := range c.ms {
			var err error
			if compiledMs[i], err = matchers.New(m.Label, matchers.Op(m.Op), m.Value); err != nil {
				t.Fatal(err)
			}
		}
		o := &order{routes: []compiled{{id: 1, matchers: compiledMs}}}
		if got := o.first(labels) == 0; got != c.want {
			t.Errorf("%+v = %v", c.ms, got)
		}
	}
	if (&order{}).first(labels) != -1 {
		t.Error("an empty order took an alert")
	}
}

// TestRouter is C-08.FR-3, FR-6, FR-8 and FR-13 with C-08.AC-1: the first Route in evaluation order takes each newly
// firing Alert and the Alert gets its Severity level; a reorder or an edit applies to the next Alerts, and an Alert
// already routed keeps its Route; a deleted Route leaves the order at once; an Alert no Route takes goes to the
// Default route; other changes are not routed.
func TestRouter(t *testing.T) {
	svc, store, _ := newService(t)
	router := newRouter(store)
	a, _ := svc.Create(t.Context(), by, input("A", Matcher{"severity", "=", "critical"}))
	b, _ := svc.Create(t.Context(), by, input("B", Matcher{"team", "=", "x"}))
	store.alerts[1] = []byte(`{"severity":"critical","team":"x","n":"1"}`)
	store.alerts[2] = []byte(`{"severity":"critical","team":"x","n":"2"}`)
	store.alerts[3] = []byte(`{"team":"y","severity":"P5"}`)
	store.alerts[4] = []byte(`{"team":"x"}`)
	store.alerts[5] = []byte(`{"severity":"critical","team":"x","n":"5"}`)
	store.alerts[6] = []byte(`{"severity":"critical","team":"x","n":"6"}`)

	changes := append(fired(1, 3, 1), ingest.AlertChange{Kind: ingest.ChangeResolved, AlertID: 4},
		ingest.AlertChange{Kind: ingest.ChangeContinued, AlertID: 4})
	got, err := router.AlertChanges(t.Context(), nil, changes)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := svc.List(t.Context())
	def := list.Routes[2]
	if !slices.Equal(got.IDs, []int64{a.ID, def.ID}) || !slices.Equal(got.PublicIDs, []string{a.PublicID, def.PublicID}) {
		t.Errorf("routed %+v", got)
	}
	if routeOfAlert(t, svc, store, 1) != "A" || routeOfAlert(t, svc, store, 3) != "Default" ||
		routeOfAlert(t, svc, store, 4) != "" {
		t.Errorf("routes %q %q %q", routeOfAlert(t, svc, store, 1), routeOfAlert(t, svc, store, 3),
			routeOfAlert(t, svc, store, 4))
	}
	if s := store.routed[1]; s.SeverityLevels[0] != "critical" || s.SeverityRaws[0] != "" || s.OrgID != orgID {
		t.Errorf("severity of 1 %+v", s)
	}
	if s := store.routed[3]; s.SeverityLevels[0] != "warning" || s.SeverityRaws[0] != "P5" {
		t.Errorf("severity of 3 %+v", s)
	}

	// C-08.AC-1: after the reorder the next Alert with both labels goes to B; the first keeps A.
	if _, err := svc.Reorder(t.Context(), by, nil, []string{b.PublicID, a.PublicID}); err != nil {
		t.Fatal(err)
	}
	if got, err = router.AlertChanges(t.Context(), nil, fired(2)); err != nil {
		t.Fatal(err)
	}
	if routeOfAlert(t, svc, store, 2) != "B" || routeOfAlert(t, svc, store, 1) != "A" ||
		!slices.Equal(got.PublicIDs, []string{b.PublicID}) {
		t.Errorf("after the reorder %q %q %+v", routeOfAlert(t, svc, store, 2), routeOfAlert(t, svc, store, 1), got)
	}

	// C-08.FR-8: an edit applies to the next Alert at once.
	in := input("B", Matcher{"team", "=", "z"})
	if _, err := svc.Update(t.Context(), by, b.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if _, err := router.AlertChanges(t.Context(), nil, fired(5)); err != nil {
		t.Fatal(err)
	}
	if routeOfAlert(t, svc, store, 5) != "A" || routeOfAlert(t, svc, store, 2) != "B" {
		t.Errorf("after the edit %q %q", routeOfAlert(t, svc, store, 5), routeOfAlert(t, svc, store, 2))
	}

	// C-08.FR-9: a deleted Route leaves the order at once.
	if err := svc.Delete(t.Context(), by, a.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := router.AlertChanges(t.Context(), nil, fired(6)); err != nil {
		t.Fatal(err)
	}
	if routeOfAlert(t, svc, store, 6) != "Default" {
		t.Errorf("after the deletion %q", routeOfAlert(t, svc, store, 6))
	}

	store.calls = map[string]int{}
	if got, err := router.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeResolved,
		AlertID: 1}}); err != nil || got.IDs != nil || len(store.calls) != 0 {
		t.Errorf("no fired change = %+v %v, queries %v", got, err, store.calls)
	}
}

// TestRouterTies: Routes at the same position are evaluated in the order they were created, every time.
func TestRouterTies(t *testing.T) {
	svc, store, _ := newService(t)
	router := newRouter(store)
	first, _ := svc.Create(t.Context(), by, input("first", Matcher{"a", "=", "1"}))
	second, _ := svc.Create(t.Context(), by, input("second", Matcher{"a", "=~", "1|2"}))
	store.find(first.PublicID).Position = 5
	store.find(second.PublicID).Position = 5
	store.order++
	store.alerts[1] = []byte(`{"a":"1"}`)
	for range 3 {
		if _, err := router.AlertChanges(t.Context(), nil, fired(1)); err != nil {
			t.Fatal(err)
		}
		if routeOfAlert(t, svc, store, 1) != "first" {
			t.Fatalf("tie went to %q", routeOfAlert(t, svc, store, 1))
		}
		router.Invalidate()
	}
}

// TestRouterCache: the order with its compiled Matchers is read once and kept while the stamp of the Routes stays the
// same; a change of any Route, or a hint, makes the next Snapshot read it again.
func TestRouterCache(t *testing.T) {
	svc, store, _ := newService(t)
	router := newRouter(store)
	a, _ := svc.Create(t.Context(), by, input("A", Matcher{"pod", "=~", "api-.*"}))
	store.alerts[1] = []byte(`{"pod":"api-1"}`)
	for range 3 {
		if _, err := router.AlertChanges(t.Context(), nil, fired(1)); err != nil {
			t.Fatal(err)
		}
	}
	if store.calls["ListRoutes"] != 1 || store.calls["GetRoutingStamp"] != 3 {
		t.Errorf("reads: %v", store.calls)
	}
	// Another replica edits the Route: its version changes the stamp although no hint arrived here.
	store.find(a.PublicID).Version++
	if _, err := router.AlertChanges(t.Context(), nil, fired(1)); err != nil {
		t.Fatal(err)
	}
	router.Invalidate()
	if _, err := router.AlertChanges(t.Context(), nil, fired(1)); err != nil {
		t.Fatal(err)
	}
	if store.calls["ListRoutes"] != 3 {
		t.Errorf("reads after a change and a hint: %v", store.calls)
	}
	if NewRouter(orgID).queries(nil) == nil {
		t.Error("no queries over the transaction")
	}
}

// TestRouterFailures: a query or data that fails stops routing with an error, which rolls the Snapshot back.
func TestRouterFailures(t *testing.T) {
	for _, query := range []string{"GetRoutingStamp", "ListRoutes", "ListRouteMatchers", "ListAlertLabels",
		"SetAlertRoutes"} {
		svc, store, _ := newService(t)
		if _, err := svc.Create(t.Context(), by, input("A", Matcher{"a", "=", "b"})); err != nil {
			t.Fatal(err)
		}
		store.alerts[1] = []byte(`{}`)
		store.fail[query] = errBoom
		if _, err := newRouter(store).AlertChanges(t.Context(), nil, fired(1)); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", query, err)
		}
	}
	for name, change := range map[string]func(*fakeStore){
		"mapping": func(s *fakeStore) { s.mapping = []byte(`{`) },
		"labels":  func(s *fakeStore) { s.alerts[1] = []byte(`[`) },
		"regex": func(s *fakeStore) {
			s.matchers[1] = []dbgen.ListRouteMatchersRow{{RouteID: 1, Label: "a", Op: "=~", Value: "("}}
		},
	} {
		_, store, _ := newService(t)
		store.alerts[1] = []byte(`{}`)
		change(store)
		if _, err := newRouter(store).AlertChanges(t.Context(), nil, fired(1)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, store, _ := newService(t)
	store.matchers[99] = []dbgen.ListRouteMatchersRow{{RouteID: 99, Label: "a", Op: "=", Value: "b"}}
	store.alerts[1] = []byte(`{}`)
	r := newRouter(store)
	r.queries = func(dbgen.DBTX) evalQueries { return strayMatchers{store} }
	if got, err := r.AlertChanges(t.Context(), nil, fired(1)); err != nil || len(got.IDs) != 1 {
		t.Errorf("a matcher of another route = %+v %v", got, err)
	}
}

// strayMatchers lists the Matchers of every Route, also those not asked for.
type strayMatchers struct{ *fakeStore }

func (s strayMatchers) ListRouteMatchers(_ context.Context, _ dbgen.ListRouteMatchersParams) (
	[]dbgen.ListRouteMatchersRow, error) {
	return s.matchers[99], nil
}
