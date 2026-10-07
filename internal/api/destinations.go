// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
)

// Destinations is what the API needs of internal/destinations: reading Destinations of every type with their health
// and Routes, and the Destinations of Routes.
type Destinations interface {
	List(ctx context.Context, f destinations.ListFilter) (destinations.Page, error)
	Get(ctx context.Context, publicID string) (destinations.Destination, error)
	RouteRefs(ctx context.Context, routeIDs []int64) (map[int64][]destinations.Ref, error)
}

// Deliveries is what the API needs of internal/delivery: the delivery state of an Alert Group per Destination.
type Deliveries interface {
	States(ctx context.Context, publicID string) ([]delivery.State, error)
}

// destinationsCursor is the cursor of the Destination list.
const destinationsCursor = "destinations"

// ListDestinations is listDestinations: Destinations of every type with their health and Routes, filtered by type,
// health and Route, in pages.
func (s *Server) ListDestinations(ctx context.Context, req gen.ListDestinationsRequestObject) (
	gen.ListDestinationsResponseObject, error) {
	p := req.Params
	f := destinations.ListFilter{Limit: pageSize(p.Limit), Route: p.Route}
	var after int64
	if ok, err := decodeCursor(p.Cursor, destinationsCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	if p.Type != nil {
		t := string(*p.Type)
		f.Type = &t
	}
	if p.Health != nil {
		h := string(*p.Health)
		f.Health = &h
	}
	page, err := s.destinations.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ListDestinations200JSONResponse{Items: make([]gen.Destination, 0, len(page.Destinations))}
	for _, d := range page.Destinations {
		item, err := destinationOf(d)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(destinationsCursor, *page.Next))
	}
	return out, nil
}

// GetDestination is getDestination; a deleted Destination reads as 404.
func (s *Server) GetDestination(ctx context.Context, req gen.GetDestinationRequestObject) (
	gen.GetDestinationResponseObject, error) {
	d, err := s.destinations.Get(ctx, req.DestinationId)
	if err != nil {
		return nil, err
	}
	body, err := destinationOf(d)
	if err != nil {
		return nil, err
	}
	tag := etag(d.Version)
	return gen.GetDestination200JSONResponse{Body: body, Headers: gen.GetDestination200ResponseHeaders{ETag: &tag}}, nil
}

// healthOf is the API form of the health of a Destination.
func healthOf(h destinations.Health) gen.DestinationHealth {
	return gen.DestinationHealth{State: gen.HealthState(h.State), Since: nullableTime(h.Since),
		Reason: nullableString(h.Reason)}
}

// destinationOf is the API form of a Destination, the variant of its type.
func destinationOf(d destinations.Destination) (gen.Destination, error) {
	tag := etag(d.Version)
	var mentions gen.MentionSettings
	if err := json.Unmarshal(d.Mentions, &mentions); err != nil {
		return gen.Destination{}, fmt.Errorf("read the mention settings of %s: %w", d.PublicID, err)
	}
	limiter := gen.Limiter{Limit: int(d.LimiterLimit), PerSeconds: int(d.LimiterPerSeconds)}
	routes := make([]gen.EntityRef, 0, len(d.Routes))
	for _, r := range d.Routes {
		routes = append(routes, gen.EntityRef{Id: r.PublicID, Name: r.Name})
	}
	var templateError *gen.TemplateErrorState
	if d.TemplateError != nil {
		templateError = &gen.TemplateErrorState{Since: d.TemplateError.Since, Error: d.TemplateError.Error,
			Fallback: gen.NotSent}
	}
	var out gen.Destination
	var err error
	switch d.Type {
	case delivery.TypeMattermost:
		err = out.FromMattermostDestination(gen.MattermostDestination{Id: d.PublicID, Name: d.Name,
			Type: gen.MattermostDestinationTypeMattermost, ConnectionId: deref(d.Connection),
			TeamId: deref(d.MattermostTeamID), ChannelId: deref(d.MattermostChannelID),
			TeamName: nullableString(d.MattermostTeamName), ChannelName: nullableString(d.MattermostChannelName),
			Mentions: mentions, Limiter: limiter, Health: healthOf(d.Health), Routes: routes,
			TemplateError: templateError, CreatedAt: d.CreatedAt, Etag: &tag})
	case delivery.TypeTelegram:
		err = out.FromTelegramDestination(gen.TelegramDestination{Id: d.PublicID, Name: d.Name,
			Type: gen.TelegramDestinationTypeTelegram, ConnectionId: deref(d.Connection),
			ChannelId: deref(d.TelegramChannelID), ChannelTitle: nullableString(d.TelegramChannelTitle),
			DiscussionGroupId:    nullableString(d.TelegramDiscussionGroupID),
			DiscussionGroupTitle: nullableString(d.TelegramDiscussionGroupTitle),
			Mentions:             mentions, Limiter: limiter, Health: healthOf(d.Health), Routes: routes,
			TemplateError: templateError, CreatedAt: d.CreatedAt, Etag: &tag})
	case delivery.TypeWebhook:
		var w gen.WebhookDestination
		if w, err = webhookOf(d); err == nil {
			w.Id, w.Name, w.Type = d.PublicID, d.Name, gen.WebhookDestinationTypeWebhook
			w.Mentions, w.Limiter, w.Health, w.Routes = mentions, limiter, healthOf(d.Health), routes
			w.TemplateError, w.CreatedAt, w.Etag = templateError, d.CreatedAt, &tag
			err = out.FromWebhookDestination(w)
		}
	default:
		err = fmt.Errorf("the destination %s has the unknown type %q", d.PublicID, d.Type)
	}
	if err != nil {
		return gen.Destination{}, err
	}
	return out, nil
}

// webhookOf is the part of an outgoing webhook that only it has: its mode, request templates, proxy and Signing
// secret, with a warning while the previous Signing secret still signs.
func webhookOf(d destinations.Destination) (gen.WebhookDestination, error) {
	w := gen.WebhookDestination{Mode: gen.WebhookDestinationMode(deref(d.WebhookMode)),
		Proxy: gen.ProxyConfig{Enabled: d.Proxy.Enabled, Address: d.Proxy.Address,
			Username:       nullableString(d.Proxy.Username),
			PasswordStatus: &gen.SecretStatus{Set: d.Proxy.PasswordSet, UpdatedAt: nullableTime(d.Proxy.PasswordUpdatedAt)}},
		SigningSecretStatus: gen.SigningSecretStatus{Set: d.SigningSecret.Set,
			UpdatedAt:           nullableTime(d.SigningSecret.UpdatedAt),
			PreviousActiveSince: nullableTime(d.SigningSecret.PreviousActiveSince)},
		Warnings: []gen.DestinationWarning{}}
	if d.Proxy.Type != nil {
		t := gen.ProxyType(*d.Proxy.Type)
		w.Proxy.Type = &t
	}
	if since := d.SigningSecret.PreviousActiveSince; since != nil {
		w.Warnings = append(w.Warnings, gen.DestinationWarning{Kind: gen.PreviousSigningSecretActive,
			Since: nullableTime(since)})
	}
	if len(d.EventsConfig) > 0 {
		w.Events = &gen.WebhookEventsConfig{}
		if err := json.Unmarshal(d.EventsConfig, w.Events); err != nil {
			return w, fmt.Errorf("read the events request of %s: %w", d.PublicID, err)
		}
	}
	if len(d.TemplateConfig) > 0 {
		w.Template = &gen.WebhookTemplateConfig{}
		if err := json.Unmarshal(d.TemplateConfig, w.Template); err != nil {
			return w, fmt.Errorf("read the request templates of %s: %w", d.PublicID, err)
		}
	}
	return w, nil
}

// errDestinationNotFound is a Destination that does not exist or is deleted.
var errDestinationNotFound = problem(http.StatusNotFound, typeNotFound, "", "No such destination.")
