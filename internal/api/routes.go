// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
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
	tag := etag(list.Version)
	return gen.ListRoutes200JSONResponse{Body: routeListOf(list), Headers: gen.ListRoutes200ResponseHeaders{ETag: &tag}},
		nil
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
	tag, location := etag(rt.Version), BasePath+"/routes/"+rt.PublicID
	return gen.CreateRoute201JSONResponse{Body: routeOf(rt),
		Headers: gen.CreateRoute201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetRoute is getRoute.
func (s *Server) GetRoute(ctx context.Context, req gen.GetRouteRequestObject) (gen.GetRouteResponseObject, error) {
	rt, err := s.routes.Get(ctx, req.RouteId)
	if err != nil {
		return nil, err
	}
	tag := etag(rt.Version)
	return gen.GetRoute200JSONResponse{Body: routeOf(rt), Headers: gen.GetRoute200ResponseHeaders{ETag: &tag}}, nil
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
	tag := etag(rt.Version)
	return gen.UpdateRoute200JSONResponse{Body: routeOf(rt), Headers: gen.UpdateRoute200ResponseHeaders{ETag: &tag}},
		nil
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
	tag := etag(list.Version)
	return gen.ReorderRoutes200JSONResponse{Body: routeListOf(list),
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

func routeListOf(list routing.List) gen.RouteList {
	out := gen.RouteList{Items: make([]gen.Route, 0, len(list.Routes))}
	for _, rt := range list.Routes {
		out.Items = append(out.Items, routeOf(rt))
	}
	return out
}

// routeOf is the API form of a Route. Destinations, the Storm state, the template error state and the count of open
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
		StormActive: false, OpenAlertGroupCount: 0, CreatedAt: rt.CreatedAt.UTC(), Etag: &tag,
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
