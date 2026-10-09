// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/webhooks"
)

// Destinations is what the API needs of internal/destinations: reading Destinations of every type with their health
// and Routes, the Destinations of Routes, saving a Destination, its Destination check, deleting a Destination, and
// its test and preview.
type Destinations interface {
	List(ctx context.Context, f destinations.ListFilter) (destinations.Page, error)
	Get(ctx context.Context, publicID string) (destinations.Destination, error)
	RouteRefs(ctx context.Context, routeIDs []int64) (map[int64][]destinations.Ref, error)
	Create(ctx context.Context, r destinations.Requester, in destinations.Input) (destinations.Destination, error)
	Update(ctx context.Context, r destinations.Requester, publicID string, version *int64, in destinations.Input) (
		destinations.Destination, error)
	Check(ctx context.Context, publicID string) (destinations.CheckResult, error)
	Delete(ctx context.Context, r destinations.Requester, publicID string, version *int64) error
	Test(ctx context.Context, r destinations.Requester, publicID string, src destinations.Source) (
		destinations.TestResult, error)
	Preview(ctx context.Context, publicID string, src destinations.Source) ([]delivery.PreviewItem, error)
}

// Webhooks is what the API needs of internal/webhooks: the Secrets and the Signing secrets of outgoing webhook
// Destinations.
type Webhooks interface {
	ListSecrets(ctx context.Context, publicID string) (webhooks.Secrets, error)
	SetSecret(ctx context.Context, r webhooks.Requester, publicID string, version *int64, name string,
		value logging.Secret) (webhooks.Secret, int64, error)
	DeleteSecret(ctx context.Context, r webhooks.Requester, publicID string, version *int64, name string) error
	SigningStatus(ctx context.Context, publicID string) (webhooks.SigningStatus, error)
	GenerateSigningSecret(ctx context.Context, r webhooks.Requester, publicID string) (logging.Secret,
		webhooks.SigningStatus, error)
	RetirePreviousSigningSecret(ctx context.Context, r webhooks.Requester, publicID string) error
}

// codeNotWebhook is the conflict of a Secret or Signing secret operation on a Destination that is not an outgoing
// webhook.
const codeNotWebhook = "not_webhook_destination"

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

func destinationRequester(ctx context.Context) (destinations.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return destinations.Requester{}, err
	}
	return destinations.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// CreateDestination is createDestination (C-13.FR-2, FR-3, C-14.FR-2): a Mattermost or Telegram Destination, saved
// once its Destination check passed on the interactive path, or an outgoing webhook.
func (s *Server) CreateDestination(ctx context.Context, req gen.CreateDestinationRequestObject) (
	gen.CreateDestinationResponseObject, error) {
	r, err := destinationRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	in, err := destinationInputOf(*req.Body)
	if err != nil {
		return nil, err
	}
	d, err := s.destinations.Create(ctx, r, in)
	if err != nil {
		return nil, templateProblem(err)
	}
	body, err := destinationOf(d)
	if err != nil {
		return nil, err
	}
	out := gen.DestinationCreated{Destination: body}
	out.SigningSecret.SetNull()
	if d.SigningSecretOnce != "" {
		out.SigningSecret.Set(string(d.SigningSecretOnce))
	}
	tag, location := etag(d.Version), BasePath+"/destinations/"+d.PublicID
	return gen.CreateDestination201JSONResponse{Body: out,
		Headers: gen.CreateDestination201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// UpdateDestination is updateDestination, with If-Match: saving a Mattermost or Telegram Destination runs its
// Destination check as createDestination does.
func (s *Server) UpdateDestination(ctx context.Context, req gen.UpdateDestinationRequestObject) (
	gen.UpdateDestinationResponseObject, error) {
	r, err := destinationRequester(ctx)
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
	in, err := destinationInputOf(*req.Body)
	if err != nil {
		return nil, err
	}
	d, err := s.destinations.Update(ctx, r, req.DestinationId, version, in)
	if errors.Is(err, destinations.ErrVersionMismatch) {
		return nil, errPreconditionFailed
	}
	if err != nil {
		return nil, templateProblem(err)
	}
	body, err := destinationOf(d)
	if err != nil {
		return nil, err
	}
	tag := etag(d.Version)
	return gen.UpdateDestination200JSONResponse{Body: body, Headers: gen.UpdateDestination200ResponseHeaders{ETag: &tag}},
		nil
}

// CheckDestination is checkDestination (C-13.FR-10, C-14.FR-14) on the interactive path: each check with its result, and the
// health after it; a passing check ends a Broken state.
func (s *Server) CheckDestination(ctx context.Context, req gen.CheckDestinationRequestObject) (
	gen.CheckDestinationResponseObject, error) {
	res, err := s.destinations.Check(ctx, req.DestinationId)
	if err != nil {
		return nil, err
	}
	out := gen.CheckDestination200JSONResponse{Ok: res.OK, Health: healthOf(res.Health),
		Checks: make([]gen.DestinationCheckItem, 0, len(res.Items))}
	for _, it := range res.Items {
		item := gen.DestinationCheckItem{Name: gen.DestinationCheckItemName(it.Name), Ok: it.OK}
		if it.Message != "" {
			item.Message.Set(it.Message)
		} else {
			item.Message.SetNull()
		}
		out.Checks = append(out.Checks, item)
	}
	return out, nil
}

// sourceOf is the source of a test or a preview.
func sourceOf(src gen.TestSource) destinations.Source {
	out := destinations.Source{Kind: string(src.Kind)}
	if id, err := src.AlertGroupId.Get(); err == nil {
		out.AlertGroupID = id
	}
	return out
}

// TestDestination is testDestination (C-16.FR-1 to FR-3, FR-5, FR-7) on the interactive path: each step with its
// masked request and response, and the health after the test. A step without a limiter token in time is limited and
// the operation still answers 200.
func (s *Server) TestDestination(ctx context.Context, req gen.TestDestinationRequestObject) (
	gen.TestDestinationResponseObject, error) {
	r, err := destinationRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	res, err := s.destinations.Test(ctx, r, req.DestinationId, sourceOf(req.Body.Source))
	if err != nil {
		return nil, err
	}
	out := gen.TestDestination200JSONResponse{Health: healthOf(res.Health),
		Steps: make([]gen.TestStep, 0, len(res.Steps))}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, testStepOf(st))
	}
	return out, nil
}

// testStepOf is the API form of a step of a test.
func testStepOf(st delivery.TestStep) gen.TestStep {
	out := gen.TestStep{Name: gen.TestStepName(st.Name), ErrorClass: gen.DeliveryErrorClass(st.ErrorClass),
		DurationMs: int(st.Duration.Milliseconds()), Request: renderedRequestOf(st.Request)}
	out.Error.SetNull()
	if st.Error != "" {
		out.Error.Set(st.Error)
	}
	out.ResponseStatus.SetNull()
	if st.ResponseStatus != 0 {
		out.ResponseStatus.Set(st.ResponseStatus)
	}
	out.ResponseBody.SetNull()
	if st.ResponseBody != nil {
		out.ResponseBody.Set(*st.ResponseBody)
	}
	if st.Extracted != nil {
		extracted := st.Extracted
		out.Extracted = &extracted
	}
	return out
}

// renderedRequestOf is the API form of a request of a test or a preview, nil for none.
func renderedRequestOf(r *delivery.TestRequest) *gen.RenderedRequest {
	if r == nil {
		return nil
	}
	out := &gen.RenderedRequest{Method: r.Method, Url: r.URL, Headers: make([]gen.HeaderTemplate, 0, len(r.Headers))}
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, gen.HeaderTemplate{Name: h[0], Value: h[1]})
	}
	out.Body.SetNull()
	if r.Body != nil {
		out.Body.Set(*r.Body)
	}
	return out
}

// PreviewDestination is previewDestination (C-16.FR-4): what the Destination would receive for the source, rendered
// without sending anything, with every Secret masked.
func (s *Server) PreviewDestination(ctx context.Context, req gen.PreviewDestinationRequestObject) (
	gen.PreviewDestinationResponseObject, error) {
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	items, err := s.destinations.Preview(ctx, req.DestinationId, sourceOf(req.Body.Source))
	if err != nil {
		return nil, err
	}
	out := gen.PreviewDestination200JSONResponse{Items: make([]gen.DestinationPreviewItem, 0, len(items))}
	for _, it := range items {
		item := gen.DestinationPreviewItem{Name: gen.DestinationPreviewItemName(it.Name),
			Request: renderedRequestOf(it.Request)}
		item.Format.SetNull()
		if it.Format != "" {
			item.Format.Set(gen.DestinationPreviewItemFormat(it.Format))
		}
		item.Text.SetNull()
		if it.Text != nil {
			item.Text.Set(*it.Text)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// templateProblem is the problem of a request template that failed on save, with its line and column; any other
// error is left to problemFor.
func templateProblem(err error) error {
	if f, ok := errors.AsType[*destinations.FieldError](err); ok && f.Line > 0 {
		p := fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
		p.Errors[0].Line, p.Errors[0].Column = positive(f.Line), positive(f.Column)
		return p
	}
	return err
}

// destinationInputOf is the Input of a Destination's body, by its type.
func destinationInputOf(body gen.DestinationInput) (destinations.Input, error) {
	kind, err := body.Discriminator()
	if err != nil {
		return destinations.Input{}, fieldProblem(http.StatusBadRequest, "/type", fieldInvalidFormat,
			"The type is not mattermost, telegram or webhook.")
	}
	switch kind {
	case delivery.TypeWebhook:
		return webhookInputOf(body)
	case delivery.TypeTelegram:
		return telegramDestinationInputOf(body)
	case delivery.TypeMattermost:
	default:
		return destinations.Input{Type: kind}, nil
	}
	m, err := body.AsMattermostDestinationInput()
	if err != nil {
		return destinations.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Mattermost Destination.")
	}
	set, err := mentionSettingsOf(m.Mentions)
	if err != nil {
		return destinations.Input{}, err
	}
	return destinations.Input{Type: kind, Name: m.Name, Mentions: set,
		Limiter: destinations.Limiter{Limit: int64(m.Limiter.Limit), PerSeconds: int64(m.Limiter.PerSeconds)},
		Mattermost: &destinations.MattermostInput{Connection: m.ConnectionId, TeamID: m.TeamId,
			ChannelID: m.ChannelId}}, nil
}

// telegramDestinationInputOf is the Input of a Telegram Destination's body: its Connection and its channel (C-14.FR-2).
func telegramDestinationInputOf(body gen.DestinationInput) (destinations.Input, error) {
	t, err := body.AsTelegramDestinationInput()
	if err != nil {
		return destinations.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Telegram Destination.")
	}
	set, err := mentionSettingsOf(t.Mentions)
	if err != nil {
		return destinations.Input{}, err
	}
	return destinations.Input{Type: delivery.TypeTelegram, Name: t.Name, Mentions: set,
		Limiter:  destinations.Limiter{Limit: int64(t.Limiter.Limit), PerSeconds: int64(t.Limiter.PerSeconds)},
		Telegram: &destinations.TelegramInput{Connection: t.ConnectionId, ChannelID: t.ChannelId}}, nil
}

// webhookInputOf is the Input of an outgoing webhook's body: its mode, the request of the events mode, the requests of
// the template mode and its proxy.
func webhookInputOf(body gen.DestinationInput) (destinations.Input, error) {
	w, err := body.AsWebhookDestinationInput()
	if err != nil {
		return destinations.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not an outgoing webhook Destination.")
	}
	set, err := mentionSettingsOf(w.Mentions)
	if err != nil {
		return destinations.Input{}, err
	}
	in := &destinations.WebhookInput{Mode: string(w.Mode), Proxy: proxyInput(w.Proxy)}
	if w.Events != nil {
		in.Events = &webhooks.EventsConfig{URL: w.Events.Url, Headers: make([]webhooks.Header, 0, len(w.Events.Headers))}
		for _, h := range w.Events.Headers {
			in.Events.Headers = append(in.Events.Headers, webhooks.Header{Name: h.Name, Value: h.Value})
		}
	}
	if t := w.Template; t != nil {
		in.Template = &webhooks.TemplateConfig{Create: requestTemplateOf(t.Create), Update: requestTemplateOf(t.Update)}
		if t.OpenThread != nil {
			r := requestTemplateOf(*t.OpenThread)
			in.Template.OpenThread = &r
		}
		if t.ReplyInThread != nil {
			r := requestTemplateOf(*t.ReplyInThread)
			in.Template.ReplyInThread = &r
		}
	}
	return destinations.Input{Type: delivery.TypeWebhook, Name: w.Name, Mentions: set,
		Limiter: destinations.Limiter{Limit: int64(w.Limiter.Limit), PerSeconds: int64(w.Limiter.PerSeconds)},
		Webhook: in}, nil
}

// requestTemplateOf is a request of the template mode as the body gives it.
func requestTemplateOf(r gen.RequestTemplate) webhooks.RequestTemplate {
	out := webhooks.RequestTemplate{Method: string(r.Method), URL: r.Url,
		Headers: make([]webhooks.Header, 0, len(r.Headers)), Extract: []webhooks.ExtractionRule{}}
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, webhooks.Header{Name: h.Name, Value: h.Value})
	}
	if r.Body.IsSpecified() && !r.Body.IsNull() {
		body := r.Body.MustGet()
		out.Body = &body
	}
	if r.Extract != nil {
		for _, e := range *r.Extract {
			out.Extract = append(out.Extract, webhooks.ExtractionRule{Name: e.Name, Path: e.Path})
		}
	}
	return out
}

// mentionSettingsOf is the Mention settings of a body, keyed by kind, with empty lists for missing ones.
func mentionSettingsOf(m gen.MentionSettings) (mentions.Settings, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var set mentions.Settings
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, err
	}
	for kind, v := range set {
		if v.UserIDs == nil {
			v.UserIDs = []string{}
		}
		if v.Groups == nil {
			v.Groups = []string{}
		}
		set[kind] = v
	}
	return set, nil
}

// DeleteDestination is deleteDestination (C-11.FR-14), with an optional If-Match: the Destination leaves every Route
// and every list, its open Root messages get their final edit and its secrets are wiped once they are done.
func (s *Server) DeleteDestination(ctx context.Context, req gen.DeleteDestinationRequestObject) (
	gen.DeleteDestinationResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	err = s.destinations.Delete(ctx, destinations.Requester{Actor: id.Actor(), Transport: id.Transport,
		Address: clientAddress(ctx)}, req.DestinationId, version)
	if errors.Is(err, destinations.ErrVersionMismatch) {
		return nil, errPreconditionFailed
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteDestination204Response{}, nil
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
		if w.Events.Headers == nil {
			w.Events.Headers = []gen.HeaderTemplate{}
		}
		c := webhooks.EventsConfig{URL: w.Events.Url}
		for _, h := range w.Events.Headers {
			c.Headers = append(c.Headers, webhooks.Header{Name: h.Name, Value: h.Value})
		}
		for _, warning := range webhooks.Warnings("/events", c) {
			item := gen.DestinationWarning{Kind: gen.DestinationWarningKind(warning.Kind)}
			item.Field.Set(warning.Field)
			w.Warnings = append(w.Warnings, item)
		}
	}
	if len(d.TemplateConfig) > 0 {
		w.Template = &gen.WebhookTemplateConfig{}
		if err := json.Unmarshal(d.TemplateConfig, w.Template); err != nil {
			return w, fmt.Errorf("read the request templates of %s: %w", d.PublicID, err)
		}
		c, err := webhooks.ParseTemplateConfig(d.TemplateConfig)
		if err != nil {
			return w, err
		}
		for _, warning := range webhooks.TemplateWarnings("/template", c) {
			item := gen.DestinationWarning{Kind: gen.DestinationWarningKind(warning.Kind)}
			item.Field.Set(warning.Field)
			w.Warnings = append(w.Warnings, item)
		}
	}
	return w, nil
}

// errDestinationNotFound is a Destination that does not exist or is deleted.
var errDestinationNotFound = problem(http.StatusNotFound, typeNotFound, "", "No such destination.")

// webhookRequester is who changes the secrets of an outgoing webhook, and how.
func webhookRequester(ctx context.Context) (webhooks.Requester, error) {
	r, err := destinationRequester(ctx)
	if err != nil {
		return webhooks.Requester{}, err
	}
	return webhooks.Requester{Actor: r.Actor, Transport: r.Transport, Address: r.Address}, nil
}

// webhookProblem maps the errors of the Secrets and Signing secrets to their problems.
func webhookProblem(err error) error {
	var f *webhooks.FieldError
	switch {
	case errors.Is(err, webhooks.ErrNotFound):
		return errDestinationNotFound
	case errors.Is(err, webhooks.ErrNotWebhook):
		return problem(http.StatusConflict, typeConflict, codeNotWebhook, "The Destination is not an outgoing webhook.")
	case errors.Is(err, webhooks.ErrNoSecret):
		return problem(http.StatusNotFound, typeNotFound, "", "No such secret.")
	case errors.Is(err, webhooks.ErrVersionMismatch):
		return errPreconditionFailed
	case errors.As(err, &f):
		return fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
	}
	return err
}

// signingStatusOf is the API form of the status of the Signing secrets.
func signingStatusOf(st webhooks.SigningStatus) gen.SigningSecretStatus {
	return gen.SigningSecretStatus{Set: st.Set, UpdatedAt: nullableTime(st.UpdatedAt),
		PreviousActiveSince: nullableTime(st.PreviousActiveSince)}
}

// optionalIfMatch is the version an optional If-Match names, nil without one.
func optionalIfMatch(v *string) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	return ifMatch(*v)
}

// ListDestinationSecrets is listDestinationSecrets (C-15.FR-10): the names of the Secrets with their status, never a
// value, and the ETag of the Secrets.
func (s *Server) ListDestinationSecrets(ctx context.Context, req gen.ListDestinationSecretsRequestObject) (
	gen.ListDestinationSecretsResponseObject, error) {
	list, err := s.webhooks.ListSecrets(ctx, req.DestinationId)
	if err != nil {
		return nil, webhookProblem(err)
	}
	out := gen.DestinationSecretList{Items: make([]gen.DestinationSecret, 0, len(list.Items))}
	for _, it := range list.Items {
		item := gen.DestinationSecret{Name: it.Name, Set: true}
		item.UpdatedAt.Set(it.UpdatedAt)
		out.Items = append(out.Items, item)
	}
	tag := etag(list.Version)
	return gen.ListDestinationSecrets200JSONResponse{Body: out,
		Headers: gen.ListDestinationSecrets200ResponseHeaders{ETag: &tag}}, nil
}

// SetDestinationSecret is setDestinationSecret (C-15.FR-10): the value is write-only; the answer is its status.
func (s *Server) SetDestinationSecret(ctx context.Context, req gen.SetDestinationSecretRequestObject) (
	gen.SetDestinationSecretResponseObject, error) {
	r, err := webhookRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || req.Body.Value == nil {
		return nil, fieldProblem(http.StatusBadRequest, "/value", fieldRequired, "The value is missing.")
	}
	sec, next, err := s.webhooks.SetSecret(ctx, r, req.DestinationId, version, req.SecretName,
		logging.Secret(*req.Body.Value))
	if err != nil {
		return nil, webhookProblem(err)
	}
	item := gen.DestinationSecret{Name: sec.Name, Set: true}
	item.UpdatedAt.Set(sec.UpdatedAt)
	tag := etag(next)
	return gen.SetDestinationSecret200JSONResponse{Body: item,
		Headers: gen.SetDestinationSecret200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteDestinationSecret is deleteDestinationSecret (C-15.FR-10).
func (s *Server) DeleteDestinationSecret(ctx context.Context, req gen.DeleteDestinationSecretRequestObject) (
	gen.DeleteDestinationSecretResponseObject, error) {
	r, err := webhookRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.webhooks.DeleteSecret(ctx, r, req.DestinationId, version, req.SecretName); err != nil {
		return nil, webhookProblem(err)
	}
	return gen.DeleteDestinationSecret204Response{}, nil
}

// GetSigningSecret is getSigningSecret (C-15.FR-5): the status of the Signing secrets, never a value.
func (s *Server) GetSigningSecret(ctx context.Context, req gen.GetSigningSecretRequestObject) (
	gen.GetSigningSecretResponseObject, error) {
	st, err := s.webhooks.SigningStatus(ctx, req.DestinationId)
	if err != nil {
		return nil, webhookProblem(err)
	}
	return gen.GetSigningSecret200JSONResponse(signingStatusOf(st)), nil
}

// GenerateSigningSecret is generateSigningSecret (C-15.FR-5): the new Signing secret, shown once; the previous one
// still signs until it is retired.
func (s *Server) GenerateSigningSecret(ctx context.Context, req gen.GenerateSigningSecretRequestObject) (
	gen.GenerateSigningSecretResponseObject, error) {
	r, err := webhookRequester(ctx)
	if err != nil {
		return nil, err
	}
	secret, st, err := s.webhooks.GenerateSigningSecret(ctx, r, req.DestinationId)
	if err != nil {
		return nil, webhookProblem(err)
	}
	return gen.GenerateSigningSecret201JSONResponse{Secret: string(secret), Status: signingStatusOf(st)}, nil
}

// RetirePreviousSigningSecret is retirePreviousSigningSecret (C-15.FR-5): the previous Signing secret signs no more.
func (s *Server) RetirePreviousSigningSecret(ctx context.Context, req gen.RetirePreviousSigningSecretRequestObject) (
	gen.RetirePreviousSigningSecretResponseObject, error) {
	r, err := webhookRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.webhooks.RetirePreviousSigningSecret(ctx, r, req.DestinationId); err != nil {
		return nil, webhookProblem(err)
	}
	return gen.RetirePreviousSigningSecret204Response{}, nil
}
