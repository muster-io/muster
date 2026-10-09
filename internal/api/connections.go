// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
)

// Connections is what the API needs of internal/connections: Mattermost and Telegram Connections, their checks and the
// channels of a Mattermost one.
type Connections interface {
	List(ctx context.Context, f connections.ListFilter) (connections.Page, error)
	Get(ctx context.Context, publicID string) (connections.Connection, error)
	Create(ctx context.Context, r connections.Requester, in connections.Input) (connections.Connection, error)
	Update(ctx context.Context, r connections.Requester, publicID string, version *int64, in connections.Input) (
		connections.Connection, error)
	Delete(ctx context.Context, r connections.Requester, publicID string, version *int64) error
	Check(ctx context.Context, publicID string, baseURL *string) (connections.CheckResult, error)
	Channels(ctx context.Context, publicID, teamID, q string) ([]connections.Channel, error)
	CallbackURL(publicID string) string
}

// connectionsCursor is the cursor of the Connection list.
const connectionsCursor = "connections"

func connectionRequester(ctx context.Context) (connections.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return connections.Requester{}, err
	}
	return connections.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListConnections is listConnections: the Connections that are not deleted, in the order they were created.
func (s *Server) ListConnections(ctx context.Context, req gen.ListConnectionsRequestObject) (
	gen.ListConnectionsResponseObject, error) {
	f := connections.ListFilter{Limit: pageSize(req.Params.Limit)}
	var after int64
	if ok, err := decodeCursor(req.Params.Cursor, connectionsCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	if req.Params.Type != nil {
		t := string(*req.Params.Type)
		f.Type = &t
	}
	page, err := s.connections.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ListConnections200JSONResponse{Items: make([]gen.Connection, 0, len(page.Connections))}
	for _, c := range page.Connections {
		item, err := s.connectionOf(c)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(connectionsCursor, *page.Next))
	}
	return out, nil
}

// CreateConnection is createConnection (C-13.FR-1, C-14.FR-1): a Mattermost or Telegram Connection; the bot token is
// write-only.
func (s *Server) CreateConnection(ctx context.Context, req gen.CreateConnectionRequestObject) (
	gen.CreateConnectionResponseObject, error) {
	r, err := connectionRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	in, err := connectionInputOf(*req.Body)
	if err != nil {
		return nil, err
	}
	c, err := s.connections.Create(ctx, r, in)
	if err != nil {
		return nil, err
	}
	body, err := s.connectionOf(c)
	if err != nil {
		return nil, err
	}
	tag, location := etag(c.Version), BasePath+"/connections/"+c.PublicID
	return gen.CreateConnection201JSONResponse{Body: body,
		Headers: gen.CreateConnection201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetConnection is getConnection; a deleted Connection reads as 404.
func (s *Server) GetConnection(ctx context.Context, req gen.GetConnectionRequestObject) (
	gen.GetConnectionResponseObject, error) {
	c, err := s.connections.Get(ctx, req.ConnectionId)
	if err != nil {
		return nil, err
	}
	body, err := s.connectionOf(c)
	if err != nil {
		return nil, err
	}
	tag := etag(c.Version)
	return gen.GetConnection200JSONResponse{Body: body, Headers: gen.GetConnection200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateConnection is updateConnection, with If-Match; an omitted bot token keeps the stored one.
func (s *Server) UpdateConnection(ctx context.Context, req gen.UpdateConnectionRequestObject) (
	gen.UpdateConnectionResponseObject, error) {
	r, err := connectionRequester(ctx)
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
	in, err := connectionInputOf(*req.Body)
	if err != nil {
		return nil, err
	}
	c, err := s.connections.Update(ctx, r, req.ConnectionId, version, in)
	if err != nil {
		return nil, err
	}
	body, err := s.connectionOf(c)
	if err != nil {
		return nil, err
	}
	tag := etag(c.Version)
	return gen.UpdateConnection200JSONResponse{Body: body, Headers: gen.UpdateConnection200ResponseHeaders{ETag: &tag}},
		nil
}

// DeleteConnection is deleteConnection (C-13.FR-6), with an optional If-Match: refused with in_use while a Destination
// that is not deleted uses the Connection.
func (s *Server) DeleteConnection(ctx context.Context, req gen.DeleteConnectionRequestObject) (
	gen.DeleteConnectionResponseObject, error) {
	r, err := connectionRequester(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	if err := s.connections.Delete(ctx, r, req.ConnectionId, version); err != nil {
		return nil, err
	}
	return gen.DeleteConnection204Response{}, nil
}

// CheckConnection is checkConnection (C-13.FR-2, C-14.FR-11) on the interactive path: the steps with their latency and
// path — the token of a Mattermost Connection; the dry probe, getMe and getWebhookInfo of a Telegram one, only the dry
// probe with an unsaved base_url — the bot's name once the token works, a Telegram bot's webhook and pending updates,
// and the warnings that do not fail the check.
func (s *Server) CheckConnection(ctx context.Context, req gen.CheckConnectionRequestObject) (
	gen.CheckConnectionResponseObject, error) {
	var baseURL *string
	if req.Body != nil && req.Body.BaseUrl.IsSpecified() && !req.Body.BaseUrl.IsNull() {
		v := req.Body.BaseUrl.MustGet()
		baseURL = &v
	}
	res, err := s.connections.Check(ctx, req.ConnectionId, baseURL)
	if err != nil {
		return nil, err
	}
	out := gen.CheckConnection200JSONResponse{Ok: res.OK, Steps: make([]gen.ConnectionCheckStep, 0, len(res.Steps)),
		Warnings: make([]gen.ConnectionCheckResultWarnings, 0, len(res.Warnings))}
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, gen.ConnectionCheckResultWarnings(w))
	}
	for _, st := range res.Steps {
		via := gen.ConnectionPath(st.Via)
		skipped := st.Skipped
		step := gen.ConnectionCheckStep{Name: gen.ConnectionCheckStepName(st.Name), Ok: st.OK, Via: &via,
			Skipped: &skipped}
		if st.Skipped {
			step.LatencyMs.SetNull()
		} else {
			step.LatencyMs.Set(int(st.Latency / time.Millisecond))
		}
		if st.Message != "" {
			step.Message.Set(st.Message)
		} else {
			step.Message.SetNull()
		}
		out.Steps = append(out.Steps, step)
	}
	if res.BotName != "" {
		out.BotName.Set(res.BotName)
	} else {
		out.BotName.SetNull()
	}
	if res.WebhookSet != nil {
		out.WebhookSet.Set(*res.WebhookSet)
	} else {
		out.WebhookSet.SetNull()
	}
	if res.PendingUpdates != nil {
		out.PendingUpdates.Set(int(*res.PendingUpdates))
	} else {
		out.PendingUpdates.SetNull()
	}
	return out, nil
}

// ListConnectionChannels is listConnectionChannels: the channels the bot of a Mattermost Connection belongs to, on the
// interactive path.
func (s *Server) ListConnectionChannels(ctx context.Context, req gen.ListConnectionChannelsRequestObject) (
	gen.ListConnectionChannelsResponseObject, error) {
	var team, q string
	if req.Params.TeamId != nil {
		team = *req.Params.TeamId
	}
	if req.Params.Q != nil {
		q = *req.Params.Q
	}
	list, err := s.connections.Channels(ctx, req.ConnectionId, team, q)
	if err != nil {
		return nil, err
	}
	out := gen.ListConnectionChannels200JSONResponse{Items: make([]gen.MattermostChannel, 0, len(list))}
	for _, c := range list {
		archived := c.DeleteAt != 0
		out.Items = append(out.Items, gen.MattermostChannel{Id: c.ID, Name: c.Name, DisplayName: c.DisplayName,
			TeamId: c.TeamID, TeamName: c.TeamName, Type: channelTypeOf(c.Type), Archived: &archived})
	}
	return out, nil
}

// channelTypeOf is the API name of a Mattermost channel type.
func channelTypeOf(t string) gen.MattermostChannelType {
	switch t {
	case mattermost.ChannelPrivate:
		return gen.MattermostChannelTypePrivate
	case mattermost.ChannelDirect:
		return gen.MattermostChannelTypeDirect
	case mattermost.ChannelGroup:
		return gen.MattermostChannelTypeGroup
	}
	return gen.MattermostChannelTypeOpen
}

// connectionInputOf is the Input of a Connection's body, Mattermost or Telegram.
func connectionInputOf(body gen.ConnectionInput) (connections.Input, error) {
	kind, err := body.Discriminator()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "/type", fieldInvalidFormat,
			"The type is not mattermost or telegram.")
	}
	switch kind {
	case connections.TypeTelegram:
		return telegramInputOf(body)
	case connections.TypeMattermost:
	default:
		return connections.Input{Type: kind}, nil
	}
	m, err := body.AsMattermostConnectionInput()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Mattermost Connection.")
	}
	in := connections.Input{Type: kind, Name: m.Name, ServerURL: m.ServerUrl, Proxy: proxyInput(m.Proxy),
		Limiter: connections.Limiter{Limit: int64(m.Limiter.Limit), PerSeconds: int64(m.Limiter.PerSeconds)}}
	if m.BotToken != nil {
		in.BotToken = keyring.Replace(logging.Secret(*m.BotToken))
	}
	return in, nil
}

// telegramInputOf is the Input of a Telegram Connection's body.
func telegramInputOf(body gen.ConnectionInput) (connections.Input, error) {
	t, err := body.AsTelegramConnectionInput()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Telegram Connection.")
	}
	in := connections.Input{Type: connections.TypeTelegram, Name: t.Name, BotAPIBaseURL: t.BotApiBaseUrl,
		UpdateMode: string(t.UpdateMode), Proxy: proxyInput(t.Proxy),
		Limiter: connections.Limiter{Limit: int64(t.Limiter.Limit), PerSeconds: int64(t.Limiter.PerSeconds)}}
	if t.BotToken != nil {
		in.BotToken = keyring.Replace(logging.Secret(*t.BotToken))
	}
	return in, nil
}

// connectionOf is the API form of a Connection: its secrets as their status, and the callback address of a Mattermost
// one or the warnings of a Telegram one.
func (s *Server) connectionOf(c connections.Connection) (gen.Connection, error) {
	if c.Type == connections.TypeTelegram {
		return telegramConnectionOf(c)
	}
	tag := etag(c.Version)
	callback := s.connections.CallbackURL(c.PublicID)
	m := gen.MattermostConnection{Id: c.PublicID, Type: gen.MattermostConnectionTypeMattermost, Name: c.Name,
		ServerUrl: c.ServerURL, BotTokenStatus: keyStatusOf(c.BotToken), Proxy: proxyOf(c.Proxy, c.ProxyPassword),
		Limiter:          gen.Limiter{Limit: int(c.Limiter.Limit), PerSeconds: int(c.Limiter.PerSeconds)},
		DestinationCount: int(c.DestinationCount), CallbackUrl: &callback, CreatedAt: c.CreatedAt, Etag: &tag}
	if c.BotUsername != nil {
		m.BotUsername.Set(*c.BotUsername)
	} else {
		m.BotUsername.SetNull()
	}
	var out gen.Connection
	if err := out.FromMattermostConnection(m); err != nil {
		return gen.Connection{}, err
	}
	return out, nil
}

// telegramConnectionOf is the API form of a Telegram Connection.
func telegramConnectionOf(c connections.Connection) (gen.Connection, error) {
	tag := etag(c.Version)
	t := gen.TelegramConnection{Id: c.PublicID, Type: gen.TelegramConnectionTypeTelegram, Name: c.Name,
		BotApiBaseUrl: c.BotAPIBaseURL, UpdateMode: gen.TelegramUpdateMode(c.UpdateMode),
		BotTokenStatus: keyStatusOf(c.BotToken), Proxy: proxyOf(c.Proxy, c.ProxyPassword),
		Limiter:          gen.Limiter{Limit: int(c.Limiter.Limit), PerSeconds: int(c.Limiter.PerSeconds)},
		DestinationCount: int(c.DestinationCount), Warnings: make([]gen.TelegramConnectionWarnings, 0, len(c.Warnings)),
		CreatedAt: c.CreatedAt, Etag: &tag}
	for _, w := range c.Warnings {
		t.Warnings = append(t.Warnings, gen.TelegramConnectionWarnings(w))
	}
	if c.BotUsername != nil {
		t.BotUsername.Set(*c.BotUsername)
	} else {
		t.BotUsername.SetNull()
	}
	var out gen.Connection
	if err := out.FromTelegramConnection(t); err != nil {
		return gen.Connection{}, err
	}
	return out, nil
}
