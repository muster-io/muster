// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

// The ids of the Route suggestions (C-08.FR-11), in the order they are listed.
const (
	SuggestionHeartbeatLost  = "heartbeat_lost"
	SuggestionInternalAlerts = "internal_alerts"
)

// The names of the Routes the suggestions create.
const (
	HeartbeatLostRouteName  = "Muster: Heartbeat lost"
	InternalAlertsRouteName = "Muster internal alerts"
)

var (
	// ErrSuggestionNotFound is an id of no Route suggestion.
	ErrSuggestionNotFound = errors.New("no such route suggestion")
	// ErrSuggestionObsolete is a Route suggestion that no longer applies.
	ErrSuggestionObsolete = errors.New("the route suggestion no longer applies")
)

// Suggestion is a Route suggestion that applies, with the Route accepting it creates at the top of the list, or for
// internal_alerts directly below the last Route other than the Default route that takes an Internal alert.
type Suggestion struct {
	ID    string
	Route Input
}

// suggested is the Route the suggestion id creates. The Route for Internal alerts takes every one of them, one Alert
// Group per Internal alert and entity, urgent only for a critical one; its Destinations are chosen on acceptance.
func suggested(id string) (Input, error) {
	switch id {
	case SuggestionHeartbeatLost:
		return Input{
			Name: HeartbeatLostRouteName,
			Matchers: []Matcher{{Label: "alertname", Op: string(matchers.Equal),
				Value: internalalerts.HeartbeatLost.Name}},
			Urgent: true, GroupKey: []string{"alertname", internalalerts.EntityIntegration},
			DestinationIDs: []string{}, Policy: onCall().Policy,
		}, nil
	case SuggestionInternalAlerts:
		return Input{
			Name:     InternalAlertsRouteName,
			Matchers: []Matcher{{Label: "alertname", Op: string(matchers.Regexp), Value: "Muster.*"}},
			GroupKey: []string{"alertname", internalalerts.EntityIntegration, internalalerts.EntityDestination,
				internalalerts.EntityRoute},
			DestinationIDs: []string{}, Policy: onCall().Policy,
		}, nil
	default:
		return Input{}, ErrSuggestionNotFound
	}
}

// Suggestions lists the Route suggestions that apply now and that the User userID, if any, has not dismissed; they
// are computed on each read.
func (s *Service) Suggestions(ctx context.Context, userID *int64) ([]Suggestion, error) {
	dismissed := map[string]bool{}
	if userID != nil {
		ids, err := s.store.ListRouteSuggestionDismissals(ctx, dbgen.ListRouteSuggestionDismissalsParams{
			OrgID: s.orgID, UserID: *userID})
		if err != nil {
			return nil, fmt.Errorf("list the dismissed route suggestions: %w", err)
		}
		for _, id := range ids {
			dismissed[id] = true
		}
	}
	out := []Suggestion{}
	for _, id := range []string{SuggestionHeartbeatLost, SuggestionInternalAlerts} {
		if dismissed[id] {
			continue
		}
		ok, _, err := s.applies(ctx, s.store, id, false)
		if err != nil {
			return nil, err
		}
		if ok {
			in, _ := suggested(id)
			out = append(out, Suggestion{ID: id, Route: in})
		}
	}
	return out, nil
}

// AcceptSuggestion creates the Route of the suggestion id at the top of the evaluation order, with destinationIDs
// when they are given, and records route.created with the suggestion; the Route for Internal alerts needs
// destinationIDs and goes directly below the last Route other than the Default route that takes an Internal alert,
// when one does (D268). A suggestion that no longer applies, checked under the lock of the list, is
// ErrSuggestionObsolete.
func (s *Service) AcceptSuggestion(ctx context.Context, r Requester, id string, destinationIDs []string) (Route,
	error) {
	in, err := suggested(id)
	if err != nil {
		return Route{}, err
	}
	if destinationIDs != nil {
		in.DestinationIDs = destinationIDs
	}
	return s.create(ctx, r, in, creation{first: true, details: map[string]any{"suggestion": id},
		precondition: func(q Queries) (int64, error) {
			ok, below, err := s.applies(ctx, q, id, true)
			switch {
			case err != nil:
				return 0, err
			case !ok:
				return 0, ErrSuggestionObsolete
			case id == SuggestionInternalAlerts && len(in.DestinationIDs) == 0:
				return 0, &FieldError{Pointer: "/destination_ids", Code: CodeRequired,
					Detail: "Choose the Destinations of the Route for Internal alerts."}
			}
			return below, nil
		}})
}

// DismissSuggestion hides the suggestion id from the User userID; a suggestion that no longer applies is
// ErrSuggestionObsolete. Dismissing it again changes nothing.
func (s *Service) DismissSuggestion(ctx context.Context, userID int64, id string) error {
	ok, _, err := s.applies(ctx, s.store, id, false)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSuggestionObsolete
	}
	if err := s.store.DismissRouteSuggestion(ctx, dbgen.DismissRouteSuggestionParams{UserID: userID, Suggestion: id,
		OrgID: s.orgID, DismissedAt: s.clock.Now().UTC()}); err != nil {
		return fmt.Errorf("dismiss the route suggestion %s: %w", id, err)
	}
	return nil
}

// applies reports whether the suggestion id applies now, read through q, and the Route, by id, to place its Route
// directly below, 0 for the top; an unknown id is ErrSuggestionNotFound.
func (s *Service) applies(ctx context.Context, q Queries, id string, fresh bool) (bool, int64, error) {
	switch id {
	case SuggestionHeartbeatLost:
		ok, err := s.heartbeatLostApplies(ctx, q, fresh)
		return ok, 0, err
	case SuggestionInternalAlerts:
		return s.internalAlertsApply(ctx, q, fresh)
	default:
		return false, 0, ErrSuggestionNotFound
	}
}

// evaluation is the current evaluation order, read through q; inside a transaction (fresh) it is read in it and
// leaves the Router's cache alone.
func (s *Service) evaluation(ctx context.Context, q Queries, fresh bool) (*order, error) {
	st, err := q.GetRoutingStamp(ctx, s.orgID)
	if err != nil {
		return nil, fmt.Errorf("read the routing stamp: %w", err)
	}
	at := stamp{order: st.RouteOrderVersion, versions: st.RouteVersions}
	if fresh {
		return s.router.load(ctx, q, at)
	}
	return s.router.order(ctx, q, at)
}

// heartbeatLostApplies reports whether heartbeat_lost applies, read through q: an Integration that is not deleted has
// its Heartbeat on, and its MusterHeartbeatLost, with the labels the Heartbeat check raises it with, would be taken
// by no Route other than the Default route in the current evaluation order. Inside a transaction (fresh) the order is
// read in it and leaves the Router's cache alone.
func (s *Service) heartbeatLostApplies(ctx context.Context, q Queries, fresh bool) (bool, error) {
	rows, err := q.ListHeartbeatIntegrations(ctx, s.orgID)
	if err != nil {
		return false, fmt.Errorf("list the integrations with a heartbeat: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}
	o, err := s.evaluation(ctx, q, fresh)
	if err != nil {
		return false, err
	}
	last := len(o.routes) - 1
	for _, row := range rows {
		static := map[string]string{}
		if err := json.Unmarshal(row.StaticLabels, &static); err != nil {
			return false, fmt.Errorf("read the static labels of %s: %w", row.PublicID, err)
		}
		labels := internalalerts.HeartbeatLost.AlertLabels(internalalerts.Entity{ID: row.PublicID, Name: row.Name},
			static)
		if i := o.first(labels); i < 0 || i == last {
			return true, nil
		}
	}
	return false, nil
}

// routeTemplates are the values of the label template of an Internal alert about a Route.
var routeTemplates = []string{TemplateRootMessage, TemplateLine, TemplateAckTimeoutNotice}

// internalAlertsApply reports whether internal_alerts applies, read through q: a Destination that is not deleted
// exists, and some Internal alert of the registry, with the labels it is raised with, would be taken by no Route other
// than the Default route in the current evaluation order. The labels are those of each entity of its kind — every
// Integration that is not deleted with its Static labels, every Destination that is not deleted, every Route, each
// with every template of a Route — or of no entity when it has none. It also returns the last Route other than the
// Default route that takes one of them, 0 for none.
func (s *Service) internalAlertsApply(ctx context.Context, q Queries, fresh bool) (bool, int64, error) {
	dests, err := q.ListAlertingDestinations(ctx, s.orgID)
	if err != nil {
		return false, 0, fmt.Errorf("list the destinations: %w", err)
	}
	if len(dests) == 0 {
		return false, 0, nil
	}
	ints, err := q.ListAlertingIntegrations(ctx, s.orgID)
	if err != nil {
		return false, 0, fmt.Errorf("list the integrations: %w", err)
	}
	o, err := s.evaluation(ctx, q, fresh)
	if err != nil {
		return false, 0, err
	}
	defs, err := internalalerts.Definitions()
	if err != nil {
		return false, 0, err
	}
	type about struct {
		entity internalalerts.Entity
		extra  map[string]string
	}
	entities := map[string][]about{}
	for _, row := range ints {
		static := map[string]string{}
		if err := json.Unmarshal(row.StaticLabels, &static); err != nil {
			return false, 0, fmt.Errorf("read the static labels of %s: %w", row.PublicID, err)
		}
		entities[internalalerts.EntityIntegration] = append(entities[internalalerts.EntityIntegration],
			about{internalalerts.Entity{ID: row.PublicID, Name: row.Name}, static})
	}
	for _, row := range dests {
		entities[internalalerts.EntityDestination] = append(entities[internalalerts.EntityDestination],
			about{entity: internalalerts.Entity{ID: row.PublicID, Name: row.Name}})
	}
	for _, r := range o.routes {
		for _, t := range routeTemplates {
			entities[internalalerts.EntityRoute] = append(entities[internalalerts.EntityRoute],
				about{internalalerts.Entity{ID: r.publicID, Name: r.name}, map[string]string{"template": t}})
		}
	}
	last, applies, below := len(o.routes)-1, false, -1
	for _, d := range defs {
		list := entities[d.Entity]
		if d.Entity == "" || len(list) == 0 {
			list = []about{{}}
		}
		for _, a := range list {
			i := o.first(d.AlertLabels(a.entity, a.extra))
			if i < 0 || i == last {
				applies = true
				continue
			}
			below = max(below, i)
		}
	}
	if below < 0 {
		return applies, 0, nil
	}
	return applies, o.routes[below].id, nil
}
