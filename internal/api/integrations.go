// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/heartbeat"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/matchers"
)

// integrationsCursor names the cursors of listIntegrations.
const integrationsCursor = "integrations"

// integrationKey is the sort key of a listIntegrations cursor.
type integrationKey struct {
	ID int64 `json:"i"`
}

// integrationRequester is who asks for a change of an Integration through the API.
func integrationRequester(ctx context.Context) (integrations.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return integrations.Requester{}, err
	}
	return integrations.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListIntegrations is listIntegrations: the Integrations that are not deleted, in the order they were created.
func (s *Server) ListIntegrations(ctx context.Context, req gen.ListIntegrationsRequestObject) (
	gen.ListIntegrationsResponseObject, error) {
	f := integrations.ListFilter{Limit: pageSize(req.Params.Limit)}
	var key integrationKey
	if ok, err := decodeCursor(req.Params.Cursor, integrationsCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &key.ID
	}
	page, err := s.integrations.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.IntegrationList{Items: make([]gen.Integration, 0, len(page.Integrations))}
	for _, in := range page.Integrations {
		out.Items = append(out.Items, s.integrationOf(in))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(integrationsCursor, integrationKey{ID: *page.Next}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListIntegrations200JSONResponse(out), nil
}

// CreateIntegration is createIntegration.
func (s *Server) CreateIntegration(ctx context.Context, req gen.CreateIntegrationRequestObject) (
	gen.CreateIntegrationResponseObject, error) {
	r, err := integrationRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	in, err := s.integrations.Create(ctx, r, integrationInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(in.Version), BasePath+"/integrations/"+in.PublicID
	return gen.CreateIntegration201JSONResponse{
		Body:    s.integrationOf(in),
		Headers: gen.CreateIntegration201ResponseHeaders{ETag: &tag, Location: &location},
	}, nil
}

// GetIntegration is getIntegration.
func (s *Server) GetIntegration(ctx context.Context, req gen.GetIntegrationRequestObject) (
	gen.GetIntegrationResponseObject, error) {
	in, err := s.integrations.Get(ctx, req.IntegrationId)
	if err != nil {
		return nil, err
	}
	tag := etag(in.Version)
	return gen.GetIntegration200JSONResponse{Body: s.integrationOf(in),
		Headers: gen.GetIntegration200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateIntegration is updateIntegration, with If-Match.
func (s *Server) UpdateIntegration(ctx context.Context, req gen.UpdateIntegrationRequestObject) (
	gen.UpdateIntegrationResponseObject, error) {
	r, err := integrationRequester(ctx)
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
	in, err := s.integrations.Update(ctx, r, req.IntegrationId, version, integrationInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(in.Version)
	return gen.UpdateIntegration200JSONResponse{Body: s.integrationOf(in),
		Headers: gen.UpdateIntegration200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteIntegration is deleteIntegration: the Integration leaves every list and its tokens stop working at once.
func (s *Server) DeleteIntegration(ctx context.Context, req gen.DeleteIntegrationRequestObject) (
	gen.DeleteIntegrationResponseObject, error) {
	r, err := integrationRequester(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	if err := s.integrations.Delete(ctx, r, req.IntegrationId, version); err != nil {
		return nil, err
	}
	return gen.DeleteIntegration204Response{}, nil
}

// ListIntegrationTokens is listIntegrationTokens: the tokens that are not revoked, never their values.
func (s *Server) ListIntegrationTokens(ctx context.Context, req gen.ListIntegrationTokensRequestObject) (
	gen.ListIntegrationTokensResponseObject, error) {
	list, err := s.integrations.ListTokens(ctx, req.IntegrationId)
	if err != nil {
		return nil, err
	}
	out := gen.IntegrationTokenList{Items: make([]gen.IntegrationToken, 0, len(list))}
	for _, t := range list {
		out.Items = append(out.Items, integrationTokenOf(t))
	}
	return gen.ListIntegrationTokens200JSONResponse(out), nil
}

// CreateIntegrationToken is createIntegrationToken: the value and the Alertmanager snippet, and the Heartbeat snippet
// while the Integration's Heartbeat is on, shown this once.
func (s *Server) CreateIntegrationToken(ctx context.Context, req gen.CreateIntegrationTokenRequestObject) (
	gen.CreateIntegrationTokenResponseObject, error) {
	r, err := integrationRequester(ctx)
	if err != nil {
		return nil, err
	}
	name := ""
	if req.Body != nil && req.Body.Name != nil {
		name = *req.Body.Name
	}
	created, err := s.integrations.CreateToken(ctx, r, req.IntegrationId, name)
	if err != nil {
		return nil, err
	}
	out := gen.IntegrationTokenCreated{Token: integrationTokenOf(created.Token), Value: created.Value,
		AlertmanagerSnippet: created.Snippet}
	if created.Heartbeat {
		out.HeartbeatSnippet.Set(heartbeat.Snippet(created.Integration, s.integrations.HeartbeatURL(), created.Value))
	} else {
		out.HeartbeatSnippet.SetNull()
	}
	return gen.CreateIntegrationToken201JSONResponse(out), nil
}

// RevokeIntegrationToken is revokeIntegrationToken: the token answers 401 from the next request.
func (s *Server) RevokeIntegrationToken(ctx context.Context, req gen.RevokeIntegrationTokenRequestObject) (
	gen.RevokeIntegrationTokenResponseObject, error) {
	r, err := integrationRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.integrations.RevokeToken(ctx, r, req.IntegrationId, req.IntegrationTokenId); err != nil {
		return nil, err
	}
	return gen.RevokeIntegrationToken204Response{}, nil
}

// ListAlertmanagerRoutes is listAlertmanagerRoutes: the Alertmanager routes Muster learned for the Integration, with
// the learned repeat interval, the time to resolve by absence and, above processing.long_repeat_warning, the warning
// and a recommended route snippet.
func (s *Server) ListAlertmanagerRoutes(ctx context.Context, req gen.ListAlertmanagerRoutesRequestObject) (
	gen.ListAlertmanagerRoutesResponseObject, error) {
	routes, err := s.alerts.Routes(ctx, req.IntegrationId)
	if err != nil {
		return nil, err
	}
	out := gen.AlertmanagerRouteList{Items: make([]gen.AlertmanagerRoute, 0, len(routes))}
	for _, r := range routes {
		item := gen.AlertmanagerRoute{RoutePath: r.RoutePath, TruncatedGroupCount: int(r.TruncatedGroupCount),
			LongIntervalWarning: r.LongIntervalWarning}
		if r.LearnedRepeatInterval != nil {
			item.LearnedRepeatIntervalSeconds.Set(int(r.LearnedRepeatInterval.Round(time.Second) / time.Second))
		} else {
			item.LearnedRepeatIntervalSeconds.SetNull()
		}
		item.ResolveByAbsenceAfterSeconds.Set(int(r.ResolveByAbsenceAfter.Round(time.Second) / time.Second))
		if r.RecommendedSnippet != "" {
			item.RecommendedSnippet.Set(r.RecommendedSnippet)
		} else {
			item.RecommendedSnippet.SetNull()
		}
		out.Items = append(out.Items, item)
	}
	return gen.ListAlertmanagerRoutes200JSONResponse(out), nil
}

// integrationAlertsCursor names the cursors of listIntegrationAlerts; the sort is part of the name, so that a cursor
// never continues a list in another order.
const integrationAlertsCursor = "integration-alerts"

// integrationAlertKey is the sort key of a listIntegrationAlerts cursor: the sorted time, then the id.
type integrationAlertKey struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// ListIntegrationAlerts is listIntegrationAlerts: the Alerts view of an Integration, filtered by state, Matchers and
// text, sorted by the last time seen (newest first by default) or startsAt.
func (s *Server) ListIntegrationAlerts(ctx context.Context, req gen.ListIntegrationAlertsRequestObject) (
	gen.ListIntegrationAlertsResponseObject, error) {
	p := req.Params
	f := ingest.AlertFilter{Integration: req.IntegrationId, Sort: ingest.SortLastSeenDesc, Limit: pageSize(p.Limit)}
	if p.Sort != nil {
		f.Sort = string(*p.Sort)
	}
	if p.State != nil {
		f.State = string(*p.State)
	}
	if p.Q != nil {
		f.Query = *p.Q
	}
	if p.Label != nil {
		for i, raw := range *p.Label {
			m, err := matchers.Parse(raw)
			if err != nil {
				return nil, matcherProblem(i, err)
			}
			f.Matchers = append(f.Matchers, m)
		}
	}
	var key integrationAlertKey
	list := integrationAlertsCursor + ":" + f.Sort
	if ok, err := decodeCursor(p.Cursor, list, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &ingest.AlertPosition{At: key.At, ID: key.ID}
	}
	page, err := s.alerts.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.IntegrationAlertList{Items: make([]gen.IntegrationAlert, 0, len(page.Alerts))}
	for _, a := range page.Alerts {
		out.Items = append(out.Items, integrationAlertOf(a))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(list, integrationAlertKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListIntegrationAlerts200JSONResponse(out), nil
}

// fieldInvalidRegex is the validation code of a Matcher whose regular expression does not compile.
const fieldInvalidRegex = "invalid_regex"

// matcherProblem is the 400 of a label Matcher that does not parse, or whose regular expression does not compile.
func matcherProblem(i int, err error) *Problem {
	pointer := fmt.Sprintf("/query/label/%d", i)
	if re, ok := errors.AsType[*matchers.RegexpError](err); ok {
		return fieldProblem(http.StatusBadRequest, pointer, fieldInvalidRegex, re.Error())
	}
	return fieldProblem(http.StatusBadRequest, pointer, fieldInvalidFormat,
		`A label filter is one Alertmanager matcher such as namespace="payments" or pod=~"api-.*".`)
}

func integrationAlertOf(a ingest.ViewAlert) gen.IntegrationAlert {
	annotations, warnings, groupKeys := gen.Labels(a.Annotations), a.Warnings, a.GroupKeys
	if annotations == nil {
		annotations = gen.Labels{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	if groupKeys == nil {
		groupKeys = []string{}
	}
	out := gen.IntegrationAlert{
		Fingerprint: a.Fingerprint, Labels: a.Labels, Annotations: &annotations, State: gen.AlertState(a.State),
		StartsAt: a.StartsAt.UTC(), LastSeenAt: a.LastSeenAt.UTC(), ResolvedAt: nullableTime(a.ResolvedAt),
		ResolveReasonText: nullableString(a.ReasonText), AlertmanagerGroups: groupKeys,
		StaticLabelWarnings: &warnings,
	}
	if a.Reason != nil {
		out.ResolveReason.Set(gen.NullableResolveReason(*a.Reason))
	} else {
		out.ResolveReason.SetNull()
	}
	if a.Route != nil {
		out.Route = &gen.EntityRef{Id: a.Route.PublicID, Name: a.Route.Name}
	}
	if a.SeverityLevel != nil {
		level := gen.SeverityLevel(*a.SeverityLevel)
		out.SeverityLevel = &level
		out.SeverityRaw = nullableString(a.SeverityRaw)
	}
	if a.AlertGroup != nil {
		out.AlertGroup = &gen.AlertGroupRef{Id: a.AlertGroup.PublicID, Number: int(a.AlertGroup.Number)}
	}
	return out
}

func integrationInputOf(in gen.IntegrationInput) integrations.Input {
	out := integrations.Input{Name: in.Name, Description: in.Description, ConnectionMode: string(in.ConnectionMode),
		StaticLabels: in.StaticLabels, DuplicateWindowSeconds: int64(in.DuplicateWindowSeconds),
		Heartbeat: integrations.HeartbeatInput{Enabled: in.Heartbeat.Enabled}}
	if in.Heartbeat.TimeoutSeconds != nil {
		t := int64(*in.Heartbeat.TimeoutSeconds)
		out.Heartbeat.TimeoutSeconds = &t
	}
	return out
}

// integrationOf is the API form of an Integration, with the URLs of the ingest listener and its warnings. The count
// of open Alert Groups arrives with them.
func (s *Server) integrationOf(in integrations.Integration) gen.Integration {
	tag := etag(in.Version)
	description, ingestURL, heartbeatURL := in.Description, s.integrations.IngestURL(), s.integrations.HeartbeatURL()
	labels := in.StaticLabels
	if labels == nil {
		labels = gen.Labels{}
	}
	out := gen.Integration{
		Id: in.PublicID, Name: in.Name, Description: &description, Builtin: in.Builtin,
		ConnectionMode: gen.IntegrationConnectionMode(in.ConnectionMode), StaticLabels: labels,
		DuplicateWindowSeconds: int(in.DuplicateWindowSeconds), IngestUrl: &ingestURL,
		Heartbeat: gen.HeartbeatInfo{Enabled: in.Heartbeat.Enabled, TimeoutSeconds: int(in.Heartbeat.TimeoutSeconds),
			State: gen.HeartbeatState(in.Heartbeat.State), Url: &heartbeatURL,
			LastSignalAt: nullableTime(in.Heartbeat.LastSignalAt), LostSince: nullableTime(in.Heartbeat.LostSince)},
		LastSnapshotAt: nullableTime(in.LastSnapshotAt), SnapshotCount: int(in.SnapshotCount),
		Warnings: make([]gen.IntegrationWarning, 0, len(in.Warnings)), OpenAlertGroupCount: 0,
		CreatedAt: in.CreatedAt.UTC(), Etag: &tag,
	}
	for _, w := range in.Warnings {
		item := gen.IntegrationWarning{Kind: gen.IntegrationWarningKind(w.Kind)}
		switch w.Kind {
		case integrations.WarningSnapshotTruncated:
			item.TruncatedGroupCount.Set(int(w.TruncatedGroupCount))
		case integrations.WarningLongRepeatInterval:
			item.RoutePath.Set(w.RoutePath)
			item.RepeatIntervalSeconds.Set(int(w.RepeatIntervalSeconds))
		case integrations.WarningHeartbeatLost:
			if w.Since != nil {
				item.Since.Set(w.Since.UTC())
			}
		}
		out.Warnings = append(out.Warnings, item)
	}
	return out
}

func integrationTokenOf(t integrations.Token) gen.IntegrationToken {
	out := gen.IntegrationToken{Id: t.PublicID, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: nullableTime(t.LastUsedAt)}
	if t.Name != "" {
		name := t.Name
		out.Name = &name
	}
	return out
}
