// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

func (s *fakeStore) ListHeartbeatIntegrations(_ context.Context, org int64) ([]dbgen.ListHeartbeatIntegrationsRow,
	error) {
	if err := s.call("ListHeartbeatIntegrations"); err != nil || org != orgID {
		return nil, err
	}
	return s.heartbeats, nil
}

func (s *fakeStore) ListRouteSuggestionDismissals(_ context.Context, arg dbgen.ListRouteSuggestionDismissalsParams) (
	[]string, error) {
	if err := s.call("ListRouteSuggestionDismissals"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	return s.dismissals[arg.UserID], nil
}

func (s *fakeStore) DismissRouteSuggestion(_ context.Context, arg dbgen.DismissRouteSuggestionParams) error {
	if err := s.call("DismissRouteSuggestion"); err != nil {
		return err
	}
	if arg.OrgID == orgID && arg.DismissedAt.Equal(t0) && !slices.Contains(s.dismissals[arg.UserID], arg.Suggestion) {
		s.dismissals[arg.UserID] = append(s.dismissals[arg.UserID], arg.Suggestion)
	}
	return nil
}

func suggestionIDs(t *testing.T, svc *Service, user *int64) []string {
	t.Helper()
	list, err := svc.Suggestions(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sg := range list {
		out = append(out, sg.ID)
	}
	return out
}

// heartbeat is an Integration with its Heartbeat on and the Static labels.
func heartbeat(id, name, static string) dbgen.ListHeartbeatIntegrationsRow {
	return dbgen.ListHeartbeatIntegrationsRow{PublicID: id, Name: name, StaticLabels: []byte(static)}
}

// TestSuggestions is C-08.FR-11 and C-08.AC-8: heartbeat_lost applies while an Integration has its Heartbeat on
// and its MusterHeartbeatLost, with the Static labels the Heartbeat check adds, would go to the Default route; it
// suggests the urgent Route for alertname="MusterHeartbeatLost" grouped by alertname and integration, with the
// On-call policy and no Destinations. internal_alerts never applies before Destinations exist.
func TestSuggestions(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	if got := suggestionIDs(t, svc, nil); len(got) != 0 {
		t.Errorf("without a heartbeat %v", got)
	}
	store.heartbeats = []dbgen.ListHeartbeatIntegrationsRow{heartbeat("NTAAAAAAAAAAAA", "prod", `{"team":"x"}`)}
	list, err := svc.Suggestions(ctx, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("suggestions %+v, %v", list, err)
	}
	in := list[0].Route
	if list[0].ID != SuggestionHeartbeatLost || in.Name != "Muster: Heartbeat lost" ||
		!slices.Equal(in.Matchers, []Matcher{{Label: "alertname", Op: "=", Value: "MusterHeartbeatLost"}}) || !in.Urgent ||
		!slices.Equal(in.GroupKey, []string{"alertname", "integration"}) || in.DestinationIDs == nil ||
		len(in.DestinationIDs) != 0 || in.Description != nil || in.Policy.StormThreshold != onCall().Policy.StormThreshold ||
		!in.Policy.AckTimeout.Enabled {
		t.Errorf("suggested %+v", in)
	}

	// A Route the Static labels make match takes it; one matching another Integration's alert does not.
	team, err := svc.Create(ctx, by, input("team-x", Matcher{Label: "team", Op: "=", Value: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestionIDs(t, svc, nil); len(got) != 0 {
		t.Errorf("taken by a Route %v", got)
	}
	store.heartbeats = append(store.heartbeats, heartbeat("NTBBBBBBBBBBBB", "stage",
		`{"alertname":"Spoofed","integration":"NTAAAAAAAAAAAA","team":"y"}`))
	if got := suggestionIDs(t, svc, nil); !slices.Equal(got, []string{SuggestionHeartbeatLost}) {
		t.Errorf("another integration %v", got)
	}
	// The labels of the Internal alert itself win over Static labels of the same name.
	spoof, err := svc.Create(ctx, by, input("spoofed", Matcher{Label: "alertname", Op: "=", Value: "Spoofed"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestionIDs(t, svc, nil); !slices.Equal(got, []string{SuggestionHeartbeatLost}) {
		t.Errorf("a static alertname %v", got)
	}
	if _, err := svc.Create(ctx, by, input("by-integration", Matcher{Label: "integration_name", Op: "=~",
		Value: "stage|prod"})); err != nil {
		t.Fatal(err)
	}
	if got := suggestionIDs(t, svc, nil); len(got) != 0 {
		t.Errorf("matched by the integration name %v", got)
	}
	for _, id := range []string{team.PublicID, spoof.PublicID, routeIDOf(t, svc, "by-integration")} {
		if err := svc.Delete(ctx, by, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := suggestionIDs(t, svc, nil); !slices.Equal(got, []string{SuggestionHeartbeatLost}) {
		t.Errorf("after the deletions %v", got)
	}
}

// TestAcceptSuggestion is C-08.FR-11, C-08.AC-8 and C-08.AC-11: accepting creates the suggested Route at the top of
// the evaluation order, bumps the list version, records route.created with the suggestion and sends the route
// hint; the suggestion no longer applies, so a second acceptance, internal_alerts and any obsolete one are
// ErrSuggestionObsolete and change nothing.
func TestAcceptSuggestion(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	store.heartbeats = []dbgen.ListHeartbeatIntegrationsRow{heartbeat("NTAAAAAAAAAAAA", "prod", `{}`)}
	if _, err := svc.Create(ctx, by, input("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, by, input("second", Matcher{Label: "team", Op: "=", Value: "y"})); err != nil {
		t.Fatal(err)
	}
	if got := suggestionIDs(t, svc, nil); len(got) != 0 {
		t.Fatalf("a catch-all Route takes it: %v", got)
	}
	if err := svc.Delete(ctx, by, routeIDOf(t, svc, "first"), nil); err != nil {
		t.Fatal(err)
	}
	before, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached := svc.router.cached
	audits, hints := len(store.audit), len(store.hints)

	rt, err := svc.AcceptSuggestion(ctx, by, SuggestionHeartbeatLost, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if names(after) != "Muster: Heartbeat lost,second,Default" || after.Version != before.Version+1 ||
		rt.Position != 0 || !rt.Urgent || rt.Name != HeartbeatLostRouteName || svc.router.cached != cached {
		t.Errorf("accepted %+v, list %s v%d", rt, names(after), after.Version)
	}
	entry := store.audit[audits]
	var details map[string]any
	if err := json.Unmarshal(entry.Details, &details); err != nil {
		t.Fatal(err)
	}
	if len(store.audit) != audits+1 || entry.Action != ActionCreated || details["suggestion"] != "heartbeat_lost" ||
		entry.ResourcePublicID.String != rt.PublicID ||
		!slices.Equal(store.hints[hints:], []db.Hint{{OrgID: orgID, Type: Hint, ID: rt.PublicID}}) {
		t.Errorf("audit %+v %s, hints %v", entry, entry.Details, store.hints[hints:])
	}
	if got := suggestionIDs(t, svc, nil); len(got) != 0 {
		t.Errorf("after accepting %v", got)
	}

	audits = len(store.audit)
	for _, id := range []string{SuggestionHeartbeatLost, SuggestionInternalAlerts} {
		if _, err := svc.AcceptSuggestion(ctx, by, id, []string{}); !errors.Is(err, ErrSuggestionObsolete) {
			t.Errorf("accept %s again: %v", id, err)
		}
	}
	if list, _ := svc.List(ctx); names(list) != names(after) || list.Version != after.Version ||
		len(store.audit) != audits {
		t.Errorf("an obsolete acceptance changed %s v%d", names(list), list.Version)
	}
	if _, err := svc.AcceptSuggestion(ctx, by, "other", nil); !errors.Is(err, ErrSuggestionNotFound) {
		t.Errorf("unknown: %v", err)
	}

	// Destinations are checked as on createRoute, and a Route with the name of the suggested one refuses it.
	if err := svc.Delete(ctx, by, rt.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AcceptSuggestion(ctx, by, SuggestionHeartbeatLost, []string{"DSAAAAAAAAAAAA"})
	if f := fieldError(t, err); f.Pointer != "/destination_ids/0" || f.Code != CodeUnknownID {
		t.Errorf("destinations %+v", f)
	}
	if _, err := svc.Create(ctx, by, input(HeartbeatLostRouteName, Matcher{Label: "team", Op: "=",
		Value: "z"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptSuggestion(ctx, by, SuggestionHeartbeatLost, nil); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
}

// TestDismissSuggestion is C-08.FR-11: a dismissal hides the suggestion from the dismissing User only, a second one
// changes nothing, and an obsolete or unknown suggestion is refused.
func TestDismissSuggestion(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	if err := svc.DismissSuggestion(ctx, 1, SuggestionHeartbeatLost); !errors.Is(err, ErrSuggestionObsolete) {
		t.Errorf("without a heartbeat: %v", err)
	}
	store.heartbeats = []dbgen.ListHeartbeatIntegrationsRow{heartbeat("NTAAAAAAAAAAAA", "prod", `{}`)}
	for range 2 {
		if err := svc.DismissSuggestion(ctx, 1, SuggestionHeartbeatLost); err != nil {
			t.Fatal(err)
		}
	}
	admin, responder := int64(1), int64(2)
	if got := suggestionIDs(t, svc, &admin); len(got) != 0 || !slices.Equal(store.dismissals[1],
		[]string{SuggestionHeartbeatLost}) {
		t.Errorf("for the dismissing user %v", got)
	}
	if got := suggestionIDs(t, svc, &responder); !slices.Equal(got, []string{SuggestionHeartbeatLost}) {
		t.Errorf("for another user %v", got)
	}
	if got := suggestionIDs(t, svc, nil); !slices.Equal(got, []string{SuggestionHeartbeatLost}) {
		t.Errorf("for a service account %v", got)
	}
	if err := svc.DismissSuggestion(ctx, 2, SuggestionInternalAlerts); !errors.Is(err, ErrSuggestionObsolete) {
		t.Errorf("internal_alerts: %v", err)
	}
	if err := svc.DismissSuggestion(ctx, 2, "other"); !errors.Is(err, ErrSuggestionNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

// TestSuggestionFailures returns the errors of the reads and writes.
func TestSuggestionFailures(t *testing.T) {
	boom := errors.New("boom")
	user := int64(1)
	for _, name := range []string{"ListHeartbeatIntegrations", "GetRoutingStamp", "ListRoutes",
		"ListRouteSuggestionDismissals"} {
		svc, store, _ := newService(t)
		store.heartbeats = []dbgen.ListHeartbeatIntegrationsRow{heartbeat("NTAAAAAAAAAAAA", "prod", `{}`)}
		store.fail[name] = boom
		if _, err := svc.Suggestions(t.Context(), &user); !errors.Is(err, boom) {
			t.Errorf("list, %s: %v", name, err)
		}
		if name == "ListRouteSuggestionDismissals" {
			continue
		}
		if err := svc.DismissSuggestion(t.Context(), 1, SuggestionHeartbeatLost); !errors.Is(err, boom) {
			t.Errorf("dismiss, %s: %v", name, err)
		}
		if _, err := svc.AcceptSuggestion(t.Context(), by, SuggestionHeartbeatLost, nil); !errors.Is(err, boom) {
			t.Errorf("accept, %s: %v", name, err)
		}
	}
	svc, store, _ := newService(t)
	store.heartbeats = []dbgen.ListHeartbeatIntegrationsRow{heartbeat("NTAAAAAAAAAAAA", "prod", `{}`)}
	store.fail["DismissRouteSuggestion"] = boom
	if err := svc.DismissSuggestion(t.Context(), 1, SuggestionHeartbeatLost); !errors.Is(err, boom) {
		t.Errorf("dismiss: %v", err)
	}
	store.heartbeats[0].StaticLabels = []byte(`[]`)
	if _, err := svc.Suggestions(t.Context(), nil); err == nil {
		t.Error("static labels that are not an object")
	}
}
