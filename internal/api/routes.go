// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

// routeRequester is who asks for a change of a Route through the API.
func routeRequester(ctx context.Context) (routing.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return routing.Requester{}, err
	}
	return routing.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListRoutes is listRoutes: every Route in evaluation order, the Default route last, with the list ETag.
func (s *Server) ListRoutes(ctx context.Context, _ gen.ListRoutesRequestObject) (gen.ListRoutesResponseObject, error) {
	list, err := s.routes.List(ctx)
	if err != nil {
		return nil, err
	}
	body, err := s.routeListView(ctx, list)
	if err != nil {
		return nil, err
	}
	tag := etag(list.Version)
	return gen.ListRoutes200JSONResponse{Body: body, Headers: gen.ListRoutes200ResponseHeaders{ETag: &tag}}, nil
}

// CreateRoute is createRoute: the Route goes before the Default route.
func (s *Server) CreateRoute(ctx context.Context, req gen.CreateRouteRequestObject) (gen.CreateRouteResponseObject,
	error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	rt, err := s.routes.Create(ctx, r, routeInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	body, err := s.routeView(ctx, rt)
	if err != nil {
		return nil, err
	}
	tag, location := etag(rt.Version), BasePath+"/routes/"+rt.PublicID
	return gen.CreateRoute201JSONResponse{Body: body,
		Headers: gen.CreateRoute201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetRoute is getRoute.
func (s *Server) GetRoute(ctx context.Context, req gen.GetRouteRequestObject) (gen.GetRouteResponseObject, error) {
	rt, err := s.routes.Get(ctx, req.RouteId)
	if err != nil {
		return nil, err
	}
	body, err := s.routeView(ctx, rt)
	if err != nil {
		return nil, err
	}
	tag := etag(rt.Version)
	return gen.GetRoute200JSONResponse{Body: body, Headers: gen.GetRoute200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateRoute is updateRoute, with If-Match; it applies to the next Alerts routed.
func (s *Server) UpdateRoute(ctx context.Context, req gen.UpdateRouteRequestObject) (gen.UpdateRouteResponseObject,
	error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	rt, err := s.routes.Update(ctx, r, req.RouteId, version, routeInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	body, err := s.routeView(ctx, rt)
	if err != nil {
		return nil, err
	}
	tag := etag(rt.Version)
	return gen.UpdateRoute200JSONResponse{Body: body, Headers: gen.UpdateRoute200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteRoute is deleteRoute: the Route leaves the evaluation order at once; the Default route cannot be deleted.
func (s *Server) DeleteRoute(ctx context.Context, req gen.DeleteRouteRequestObject) (gen.DeleteRouteResponseObject,
	error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	if err := s.routes.Delete(ctx, r, req.RouteId, version); err != nil {
		return nil, err
	}
	return gen.DeleteRoute204Response{}, nil
}

// MoveOpenAlertGroups is moveOpenAlertGroups: the open Alert Groups of the Route move to the Default route, after
// which the Route can be deleted.
func (s *Server) MoveOpenAlertGroups(ctx context.Context, req gen.MoveOpenAlertGroupsRequestObject) (
	gen.MoveOpenAlertGroupsResponseObject, error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	moved, err := s.alertGroups.MoveOpenAlertGroups(ctx, groups.Requester{Actor: r.Actor, Transport: r.Transport,
		Address: r.Address}, req.RouteId)
	if err != nil {
		return nil, err
	}
	return gen.MoveOpenAlertGroups200JSONResponse{Moved: moved}, nil
}

// ReorderRoutes is reorderRoutes: If-Match carries the list ETag of listRoutes.
func (s *Server) ReorderRoutes(ctx context.Context, req gen.ReorderRoutesRequestObject) (
	gen.ReorderRoutesResponseObject, error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	list, err := s.routes.Reorder(ctx, r, version, req.Body.RouteIds)
	if err != nil {
		return nil, err
	}
	body, err := s.routeListView(ctx, list)
	if err != nil {
		return nil, err
	}
	tag := etag(list.Version)
	return gen.ReorderRoutes200JSONResponse{Body: body,
		Headers: gen.ReorderRoutes200ResponseHeaders{ETag: &tag}}, nil
}

// ListRouteProfiles is listRouteProfiles: On-call and Informational, from the built-in defaults.
func (s *Server) ListRouteProfiles(context.Context, gen.ListRouteProfilesRequestObject) (
	gen.ListRouteProfilesResponseObject, error) {
	profiles := routing.Profiles()
	out := gen.RouteProfileList{Items: make([]gen.RouteProfile, 0, len(profiles))}
	for _, p := range profiles {
		out.Items = append(out.Items, gen.RouteProfile{Id: gen.RouteProfileId(p.ID), Name: p.Name, Urgent: p.Urgent,
			GroupKey: p.GroupKey, Policy: routePolicyOf(p.Policy)})
	}
	return gen.ListRouteProfiles200JSONResponse(out), nil
}

// PreviewGroupKey is previewGroupKey: how the Stored Snapshots of the period group with the current and the proposed
// Group key.
func (s *Server) PreviewGroupKey(ctx context.Context, req gen.PreviewGroupKeyRequestObject) (
	gen.PreviewGroupKeyResponseObject, error) {
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	in := routing.PreviewRequest{ProposedGroupKey: req.Body.ProposedGroupKey}
	if req.Body.RouteId.IsSpecified() && !req.Body.RouteId.IsNull() {
		in.RouteID = req.Body.RouteId.MustGet()
	}
	if req.Body.Matchers != nil {
		in.Matchers = make([]routing.Matcher, 0, len(*req.Body.Matchers))
		for _, m := range *req.Body.Matchers {
			in.Matchers = append(in.Matchers, routing.Matcher{Label: m.Label, Op: string(m.Op), Value: m.Value})
		}
	}
	if req.Body.PeriodSeconds != nil {
		period := int64(*req.Body.PeriodSeconds)
		in.PeriodSeconds = &period
	}
	p, err := s.routes.Preview(ctx, in)
	if err != nil {
		return nil, err
	}
	out := gen.GroupKeyPreview{PeriodSeconds: int(p.PeriodSeconds), Truncated: p.Truncated,
		Proposed: previewSideOf(p.Proposed)}
	if p.Current != nil {
		current := previewSideOf(*p.Current)
		out.Current = &current
	}
	return gen.PreviewGroupKey200JSONResponse(out), nil
}

func previewSideOf(side routing.PreviewSide) gen.GroupKeyPreviewSide {
	out := gen.GroupKeyPreviewSide{AlertGroupCount: side.AlertGroupCount,
		Examples: make([]gen.GroupKeyPreviewExample, 0, len(side.Examples))}
	for _, e := range side.Examples {
		out.Examples = append(out.Examples, gen.GroupKeyPreviewExample{GroupKeyValues: e.GroupKeyValues,
			AlertCount: e.AlertCount})
	}
	return out
}

// ListRouteSuggestions is listRouteSuggestions: the suggestions that apply and that the calling User has not
// dismissed; a Service account has no dismissals.
func (s *Server) ListRouteSuggestions(ctx context.Context, _ gen.ListRouteSuggestionsRequestObject) (
	gen.ListRouteSuggestionsResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	var user *int64
	if !id.IsServiceAccount() {
		user = &id.Session.User.ID
	}
	list, err := s.routes.Suggestions(ctx, user)
	if err != nil {
		return nil, err
	}
	out := gen.RouteSuggestionList{Items: make([]gen.RouteSuggestion, 0, len(list))}
	for _, sg := range list {
		out.Items = append(out.Items, gen.RouteSuggestion{Id: gen.RouteSuggestionId(sg.ID), Route: routeInputFor(sg.Route)})
	}
	return gen.ListRouteSuggestions200JSONResponse(out), nil
}

// AcceptRouteSuggestion is acceptRouteSuggestion: the suggested Route at the top of the list.
func (s *Server) AcceptRouteSuggestion(ctx context.Context, req gen.AcceptRouteSuggestionRequestObject) (
	gen.AcceptRouteSuggestionResponseObject, error) {
	r, err := routeRequester(ctx)
	if err != nil {
		return nil, err
	}
	var destinations []string
	if req.Body != nil && req.Body.DestinationIds != nil {
		destinations = *req.Body.DestinationIds
	}
	rt, err := s.routes.AcceptSuggestion(ctx, r, string(req.SuggestionId), destinations)
	if err != nil {
		return nil, err
	}
	body, err := s.routeView(ctx, rt)
	if err != nil {
		return nil, err
	}
	tag, location := etag(rt.Version), BasePath+"/routes/"+rt.PublicID
	return gen.AcceptRouteSuggestion201JSONResponse{Body: body,
		Headers: gen.AcceptRouteSuggestion201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// DismissRouteSuggestion is dismissRouteSuggestion: remembered for the calling User, so a Service account is refused.
func (s *Server) DismissRouteSuggestion(ctx context.Context, req gen.DismissRouteSuggestionRequestObject) (
	gen.DismissRouteSuggestionResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if id.IsServiceAccount() {
		return nil, errServiceAccountDenied
	}
	if err := s.routes.DismissSuggestion(ctx, id.Session.User.ID, string(req.SuggestionId)); err != nil {
		return nil, err
	}
	return gen.DismissRouteSuggestion204Response{}, nil
}

// routeInputFor is the API form of a Route to create.
func routeInputFor(in routing.Input) gen.RouteInput {
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	out := gen.RouteInput{Name: in.Name, Description: &description, Urgent: in.Urgent, GroupKey: in.GroupKey,
		DestinationIds: in.DestinationIDs, Matchers: make([]gen.Matcher, 0, len(in.Matchers)),
		Policy: routePolicyOf(in.Policy)}
	if out.DestinationIds == nil {
		out.DestinationIds = []gen.PublicId{}
	}
	for _, m := range in.Matchers {
		out.Matchers = append(out.Matchers, gen.Matcher{Label: m.Label, Op: gen.MatcherOp(m.Op), Value: m.Value})
	}
	return out
}

func routeListOf(list routing.List) gen.RouteList {
	out := gen.RouteList{Items: make([]gen.Route, 0, len(list.Routes))}
	for _, rt := range list.Routes {
		out.Items = append(out.Items, routeOf(rt))
	}
	return out
}

// routeOf is the API form of a Route without its Destinations, which routeView adds. The Storm state, the template error state and the count of open
// Alert Groups arrive with their capabilities.
func routeOf(rt routing.Route) gen.Route {
	tag, description := etag(rt.Version), rt.Description
	key := rt.GroupKey
	if key == nil {
		key = []string{}
	}
	out := gen.Route{
		Id: rt.PublicID, Name: rt.Name, Description: &description, Position: rt.Position, IsDefault: rt.IsDefault,
		Urgent: rt.Urgent, GroupKey: key, Matchers: make([]gen.Matcher, 0, len(rt.Matchers)),
		DestinationIds: []gen.PublicId{}, Destinations: []gen.DestinationRef{}, Policy: routePolicyOf(rt.Policy),
		StormActive: false, OpenAlertGroupCount: int(rt.OpenAlertGroupCount), CreatedAt: rt.CreatedAt.UTC(), Etag: &tag,
	}
	for _, m := range rt.Matchers {
		out.Matchers = append(out.Matchers, gen.Matcher{Label: m.Label, Op: gen.MatcherOp(m.Op), Value: m.Value})
	}
	return out
}

func routePolicyOf(p routing.Policy) gen.RoutePolicy {
	snooze := make([]int, len(p.SnoozeDurationsSeconds))
	for i, d := range p.SnoozeDurationsSeconds {
		snooze[i] = int(d)
	}
	return gen.RoutePolicy{
		ReopenWindowSeconds: int(p.ReopenWindowSeconds), GracePeriodSeconds: int(p.GracePeriodSeconds),
		UrgentRiseRemovesAck: p.UrgentRiseRemovesAck, SnoozeDurationsSeconds: snooze,
		ThreadBatchingWindowSeconds: int(p.ThreadBatchingWindowSeconds), StormThreshold: int(p.StormThreshold),
		Language: gen.Language(p.Language),
		Templates: gen.RouteTemplates{RootMessage: nullableString(p.Templates.RootMessage),
			Line: nullableString(p.Templates.Line), AckTimeoutNotice: nullableString(p.Templates.AckTimeoutNotice)},
		AckTimeout: gen.AckTimeoutPolicy{Enabled: p.AckTimeout.Enabled,
			FirstIntervalSeconds: int(p.AckTimeout.FirstIntervalSeconds)},
		Reminders: gen.RemindersPolicy{Enabled: p.Reminders.Enabled,
			FirstIntervalSeconds: int(p.Reminders.FirstIntervalSeconds), CapSeconds: int(p.Reminders.CapSeconds)},
		AutoUnacknowledge: p.AutoUnacknowledge,
	}
}

func routeInputOf(in gen.RouteInput) routing.Input {
	out := routing.Input{Name: in.Name, Description: in.Description, Urgent: in.Urgent, GroupKey: in.GroupKey,
		DestinationIDs: in.DestinationIds, Policy: routingPolicyOf(in.Policy)}
	for _, m := range in.Matchers {
		out.Matchers = append(out.Matchers, routing.Matcher{Label: m.Label, Op: string(m.Op), Value: m.Value})
	}
	return out
}

func routingPolicyOf(p gen.RoutePolicy) routing.Policy {
	snooze := make([]int64, len(p.SnoozeDurationsSeconds))
	for i, d := range p.SnoozeDurationsSeconds {
		snooze[i] = int64(d)
	}
	return routing.Policy{
		ReopenWindowSeconds: int64(p.ReopenWindowSeconds), GracePeriodSeconds: int64(p.GracePeriodSeconds),
		UrgentRiseRemovesAck: p.UrgentRiseRemovesAck, SnoozeDurationsSeconds: snooze,
		ThreadBatchingWindowSeconds: int64(p.ThreadBatchingWindowSeconds), StormThreshold: int64(p.StormThreshold),
		Language: string(p.Language),
		Templates: routing.Templates{RootMessage: templateOf(p.Templates.RootMessage),
			Line: templateOf(p.Templates.Line), AckTimeoutNotice: templateOf(p.Templates.AckTimeoutNotice)},
		AckTimeout: routing.AckTimeout{Enabled: p.AckTimeout.Enabled,
			FirstIntervalSeconds: int64(p.AckTimeout.FirstIntervalSeconds)},
		Reminders: routing.Reminders{Enabled: p.Reminders.Enabled,
			FirstIntervalSeconds: int64(p.Reminders.FirstIntervalSeconds), CapSeconds: int64(p.Reminders.CapSeconds)},
		AutoUnacknowledge: p.AutoUnacknowledge,
	}
}

// templateOf is a template as given: nil when it is absent or null, which is the built-in template.
func templateOf(v nullable.Nullable[string]) *string {
	if !v.IsSpecified() || v.IsNull() {
		return nil
	}
	s := v.MustGet()
	return &s
}

// fillDestinations sets the Destinations of the API forms out of the Routes rts, one for one, with their health
// (C-08.FR-1).
func (s *Server) fillDestinations(ctx context.Context, rts []routing.Route, out []gen.Route) error {
	if s.destinations == nil || len(rts) == 0 {
		return nil
	}
	ids := make([]int64, len(rts))
	for i, rt := range rts {
		ids[i] = rt.ID
	}
	refs, err := s.destinations.RouteRefs(ctx, ids)
	if err != nil {
		return err
	}
	for i, rt := range rts {
		o := &out[i]
		for _, r := range refs[rt.ID] {
			o.DestinationIds = append(o.DestinationIds, r.PublicID)
			o.Destinations = append(o.Destinations, gen.DestinationRef{Id: r.PublicID, Name: r.Name,
				Type: gen.DestinationType(r.Type), Health: healthOf(r.Health)})
		}
	}
	return nil
}

// routeView is the API form of one Route with its Destinations.
func (s *Server) routeView(ctx context.Context, rt routing.Route) (gen.Route, error) {
	out := []gen.Route{routeOf(rt)}
	if err := s.fillDestinations(ctx, []routing.Route{rt}, out); err != nil {
		return gen.Route{}, err
	}
	return out[0], nil
}

// routeListView is the API form of the Route list with their Destinations.
func (s *Server) routeListView(ctx context.Context, list routing.List) (gen.RouteList, error) {
	out := routeListOf(list)
	if err := s.fillDestinations(ctx, list.Routes, out.Items); err != nil {
		return gen.RouteList{}, err
	}
	return out, nil
}
