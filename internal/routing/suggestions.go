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

// HeartbeatLostRouteName is the name of the Route the heartbeat_lost suggestion creates.
const HeartbeatLostRouteName = "Muster: Heartbeat lost"

var (
	// ErrSuggestionNotFound is an id of no Route suggestion.
	ErrSuggestionNotFound = errors.New("no such route suggestion")
	// ErrSuggestionObsolete is a Route suggestion that no longer applies.
	ErrSuggestionObsolete = errors.New("the route suggestion no longer applies")
)

// Suggestion is a Route suggestion that applies, with the Route accepting it creates at the top of the list.
type Suggestion struct {
	ID    string
	Route Input
}

// suggested is the Route the suggestion id creates; internal_alerts needs a Destination, which no capability offers
// yet (C-13.FR-11), so it never applies.
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
		return Input{}, ErrSuggestionObsolete
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
		ok, err := s.applies(ctx, s.store, id, false)
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
// when they are given, and records route.created with the suggestion. A suggestion that no longer applies, checked
// under the lock of the list, is ErrSuggestionObsolete.
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
		precondition: func(q Queries) error {
			ok, err := s.applies(ctx, q, id, true)
			if err == nil && !ok {
				err = ErrSuggestionObsolete
			}
			return err
		}})
}

// DismissSuggestion hides the suggestion id from the User userID; a suggestion that no longer applies is
// ErrSuggestionObsolete. Dismissing it again changes nothing.
func (s *Service) DismissSuggestion(ctx context.Context, userID int64, id string) error {
	ok, err := s.applies(ctx, s.store, id, false)
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

// applies reports whether the suggestion id applies now, read through q; an unknown id is ErrSuggestionNotFound.
func (s *Service) applies(ctx context.Context, q Queries, id string, fresh bool) (bool, error) {
	switch id {
	case SuggestionHeartbeatLost:
		return s.heartbeatLostApplies(ctx, q, fresh)
	case SuggestionInternalAlerts:
		return false, nil
	default:
		return false, ErrSuggestionNotFound
	}
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
	st, err := q.GetRoutingStamp(ctx, s.orgID)
	if err != nil {
		return false, fmt.Errorf("read the routing stamp: %w", err)
	}
	at := stamp{order: st.RouteOrderVersion, versions: st.RouteVersions}
	var o *order
	if fresh {
		o, err = s.router.load(ctx, q, at)
	} else {
		o, err = s.router.order(ctx, q, at)
	}
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
