// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/slack"
)

// Connections is what the API needs of internal/connections: Mattermost, Telegram, Bridge and Slack Connections, their
// checks, the checks of their drafts and the channels of a Mattermost or Slack one.
type Connections interface {
	List(ctx context.Context, f connections.ListFilter) (connections.Page, error)
	Get(ctx context.Context, publicID string) (connections.Connection, error)
	Create(ctx context.Context, r connections.Requester, in connections.Input) (connections.Connection, error)
	Update(ctx context.Context, r connections.Requester, publicID string, version *int64, in connections.Input) (
		connections.Connection, error)
	Delete(ctx context.Context, r connections.Requester, publicID string, version *int64) error
	Check(ctx context.Context, publicID string, baseURL *string) (connections.CheckResult, error)
	CheckDraft(ctx context.Context, d connections.Draft) (connections.CheckResult, error)
	Channels(ctx context.Context, publicID, teamID, q string) ([]connections.Channel, error)
	SlackChannels(ctx context.Context, publicID, q string) (slack.ChannelList, error)
	CallbackURL(publicID string) string
	BridgeCallbackURL(publicID string) string
	SlackCallbackURL(publicID string) string
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

// CreateConnection is createConnection (C-13.FR-1, C-14.FR-1, C-22.FR-1, C-24.FR-1): a Mattermost, Telegram, Bridge or
// Slack Connection; the bot token, the push secret, the app-level token and the Slack signing secret are write-only.
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

// UpdateConnection is updateConnection, with If-Match; an omitted bot token, push secret, app-level token or Slack
// signing secret keeps the stored one.
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

// CheckConnection is checkConnection (C-13.FR-2, C-14.FR-11, C-22.FR-2, C-24.FR-2) on the interactive path: the steps
// with their latency and path — the token of a Mattermost Connection; the dry probe, getMe and getWebhookInfo of a
// Telegram one, only the dry probe with an unsaved base_url; info and event_delivery of a Bridge; token, scopes and in
// socket mode app_token of a Slack one — the bot's name once the token works, a Telegram bot's webhook and pending
// updates, the info a Bridge's check read, null when it failed, what a Slack check found, null when its token failed,
// and the warnings that do not fail the check. A step's message is English; its code is what clients translate.
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
	return gen.CheckConnection200JSONResponse(checkResultOf(res)), nil
}

// CheckConnectionDraft is checkConnectionDraft (C-13.FR-14) on the interactive path: the check of a Connection of any
// type as its form holds it, with the steps of checkConnection, storing nothing; which token goes where is the rule of
// connections.Service.CheckDraft.
func (s *Server) CheckConnectionDraft(ctx context.Context, req gen.CheckConnectionDraftRequestObject) (
	gen.CheckConnectionDraftResponseObject, error) {
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	d, err := connectionDraftOf(*req.Body)
	if err != nil {
		return nil, err
	}
	res, err := s.connections.CheckDraft(ctx, d)
	if err != nil {
		return nil, err
	}
	return gen.CheckConnectionDraft200JSONResponse(checkResultOf(res)), nil
}

// connectionDraftOf is the Draft of a draft's body, Mattermost, Telegram, Bridge or Slack: a bot token, and a Slack
// app-level token, is the one typed in the form, an omitted proxy the rule of the draft's Connection.
func connectionDraftOf(body gen.ConnectionDraft) (connections.Draft, error) {
	kind, err := body.Discriminator()
	if err != nil {
		return connections.Draft{}, fieldProblem(http.StatusBadRequest, "/type", fieldInvalidFormat,
			"The type is not mattermost, telegram, bridge or slack.")
	}
	var d connections.Draft
	var token *string
	var p *gen.ProxyConfigInput
	switch kind {
	case connections.TypeMattermost:
		m, err := body.AsMattermostConnectionDraft()
		if err != nil {
			return connections.Draft{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
				"The body is not a Mattermost Connection.")
		}
		d = connections.Draft{Type: kind, ServerURL: m.ServerUrl, ConnectionID: m.ConnectionId}
		token, p = m.BotToken, m.Proxy
	case connections.TypeTelegram:
		t, err := body.AsTelegramConnectionDraft()
		if err != nil {
			return connections.Draft{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
				"The body is not a Telegram Connection.")
		}
		d = connections.Draft{Type: kind, BotAPIBaseURL: t.BotApiBaseUrl, UpdateMode: string(t.UpdateMode),
			ConnectionID: t.ConnectionId}
		token, p = t.BotToken, t.Proxy
	case connections.TypeBridge:
		b, err := body.AsBridgeConnectionDraft()
		if err != nil {
			return connections.Draft{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
				"The body is not a Bridge Connection.")
		}
		d = connections.Draft{Type: kind, BridgeBaseURL: b.BaseUrl, EventDelivery: string(b.EventDelivery),
			ConnectionID: b.ConnectionId}
		token, p = b.BotToken, b.Proxy
	case connections.TypeSlack:
		sl, err := body.AsSlackConnectionDraft()
		if err != nil {
			return connections.Draft{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
				"The body is not a Slack Connection.")
		}
		d = connections.Draft{Type: kind, EventMode: string(sl.EventMode), ConnectionID: sl.ConnectionId}
		if sl.AppToken != nil {
			d.AppToken = keyring.Replace(logging.Secret(*sl.AppToken))
		}
		token, p = sl.BotToken, sl.Proxy
	default:
		return connections.Draft{Type: kind}, nil
	}
	if token != nil {
		d.BotToken = keyring.Replace(logging.Secret(*token))
	}
	if p != nil {
		in := proxyInput(*p)
		d.Proxy = &in
	}
	return d, nil
}

// checkResultOf is the API form of the result of a Connection check, of a saved Connection or of a draft: the steps,
// without latency those that made no call (connections.Step.Called), with the scopes a missing_scope names (D396), the
// bot's name, the roles of a Mattermost bot read with the warning press_answers_in_thread, a Telegram bot's webhook
// and pending updates, a Bridge's info, what a Slack check found, null otherwise, and the check run of its requests.
func checkResultOf(res connections.CheckResult) gen.ConnectionCheckResult {
	out := gen.ConnectionCheckResult{Ok: res.OK, Steps: make([]gen.ConnectionCheckStep, 0, len(res.Steps)),
		Warnings: make([]gen.ConnectionCheckResultWarnings, 0, len(res.Warnings))}
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, gen.ConnectionCheckResultWarnings(w))
	}
	for _, st := range res.Steps {
		via := gen.ConnectionPath(st.Via)
		skipped := st.Skipped
		step := gen.ConnectionCheckStep{Name: gen.ConnectionCheckStepName(st.Name), Ok: st.OK, Via: &via,
			Skipped: &skipped}
		if st.Called() {
			step.LatencyMs.Set(int(st.Latency / time.Millisecond))
		} else {
			step.LatencyMs.SetNull()
		}
		if st.Message != "" {
			step.Message.Set(st.Message)
		} else {
			step.Message.SetNull()
		}
		if st.Code != "" {
			step.Code.Set(gen.NullableConnectionCheckCode(st.Code))
		} else {
			step.Code.SetNull()
		}
		if len(st.MissingScopes) > 0 {
			scopes := slices.Clone(st.MissingScopes)
			step.MissingScopes = &scopes
		}
		out.Steps = append(out.Steps, step)
	}
	if res.BotName != "" {
		out.BotName.Set(res.BotName)
	} else {
		out.BotName.SetNull()
	}
	if res.BotRoles != nil {
		roles := slices.Clone(res.BotRoles)
		out.BotRoles = &roles
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
	if res.Bridge {
		out.Info = bridgeInfoOf(res.Info)
	}
	out.Slack = slackCheckInfoOf(res.Slack)
	out.RequestLogRun = res.Run
	return out
}

// ListConnectionChannels is listConnectionChannels on the interactive path: the channels the bot of a Mattermost
// Connection belongs to, team by team, as MattermostChannelList, or those of a Slack Connection as SlackChannelList
// (C-24.FR-3), whose team_id is ignored; a Telegram or Bridge Connection answers 409.
func (s *Server) ListConnectionChannels(ctx context.Context, req gen.ListConnectionChannelsRequestObject) (
	gen.ListConnectionChannelsResponseObject, error) {
	var team, q string
	if req.Params.TeamId != nil {
		team = *req.Params.TeamId
	}
	if req.Params.Q != nil {
		q = *req.Params.Q
	}
	c, err := s.connections.Get(ctx, req.ConnectionId)
	if err != nil {
		return nil, err
	}
	var body gen.ConnectionChannelList
	switch c.Type {
	case connections.TypeSlack:
		list, err := s.connections.SlackChannels(ctx, req.ConnectionId, q)
		if err != nil {
			return nil, err
		}
		out := gen.SlackChannelList{Items: make([]gen.SlackChannel, 0, len(list.Channels)), Truncated: list.Truncated}
		for _, ch := range list.Channels {
			out.Items = append(out.Items, gen.SlackChannel{Id: ch.ID, Name: ch.Name, IsPrivate: ch.Private})
		}
		if err := body.FromSlackChannelList(out); err != nil {
			return nil, err
		}
	case connections.TypeMattermost:
		list, err := s.connections.Channels(ctx, req.ConnectionId, team, q)
		if err != nil {
			return nil, err
		}
		out := gen.MattermostChannelList{Items: make([]gen.MattermostChannel, 0, len(list))}
		for _, ch := range list {
			archived := ch.DeleteAt != 0
			out.Items = append(out.Items, gen.MattermostChannel{Id: ch.ID, Name: ch.Name, DisplayName: ch.DisplayName,
				TeamId: ch.TeamID, TeamName: ch.TeamName, Type: channelTypeOf(ch.Type), Archived: &archived})
		}
		if err := body.FromMattermostChannelList(out); err != nil {
			return nil, err
		}
	default:
		return nil, connections.ErrNotMattermost
	}
	return gen.ListConnectionChannels200JSONResponse(body), nil
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

// connectionInputOf is the Input of a Connection's body, Mattermost, Telegram, Bridge or Slack.
func connectionInputOf(body gen.ConnectionInput) (connections.Input, error) {
	kind, err := body.Discriminator()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "/type", fieldInvalidFormat,
			"The type is not mattermost, telegram, bridge or slack.")
	}
	switch kind {
	case connections.TypeTelegram:
		return telegramInputOf(body)
	case connections.TypeBridge:
		return bridgeInputOf(body)
	case connections.TypeSlack:
		return slackInputOf(body)
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

// bridgeInputOf is the Input of a Bridge Connection's body.
func bridgeInputOf(body gen.ConnectionInput) (connections.Input, error) {
	b, err := body.AsBridgeConnectionInput()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Bridge Connection.")
	}
	in := connections.Input{Type: connections.TypeBridge, Name: b.Name, BridgeBaseURL: b.BaseUrl,
		EventDelivery: string(b.EventDelivery), Proxy: proxyInput(b.Proxy),
		Limiter: connections.Limiter{Limit: int64(b.Limiter.Limit), PerSeconds: int64(b.Limiter.PerSeconds)}}
	if b.BotToken != nil {
		in.BotToken = keyring.Replace(logging.Secret(*b.BotToken))
	}
	if b.PushSecret != nil {
		in.PushSecret = keyring.Replace(logging.Secret(*b.PushSecret))
	}
	return in, nil
}

// connectionOf is the API form of a Connection: its secrets as their status, and the callback address of a Mattermost
// one, the warnings of a Telegram one, the callback address and the last info of a Bridge, or the callback address
// and what the last check of a Slack one learned.
func (s *Server) connectionOf(c connections.Connection) (gen.Connection, error) {
	switch c.Type {
	case connections.TypeTelegram:
		return telegramConnectionOf(c)
	case connections.TypeBridge:
		return s.bridgeConnectionOf(c)
	case connections.TypeSlack:
		return s.slackConnectionOf(c)
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

// bridgeConnectionOf is the API form of a Bridge Connection: its info and when it was read are null before its first
// check and after its base URL or token changed.
func (s *Server) bridgeConnectionOf(c connections.Connection) (gen.Connection, error) {
	tag := etag(c.Version)
	callback := s.connections.BridgeCallbackURL(c.PublicID)
	b := gen.BridgeConnection{Id: c.PublicID, Type: gen.BridgeConnectionTypeBridge, Name: c.Name,
		BaseUrl: c.BridgeBaseURL, EventDelivery: gen.BridgeEventDelivery(c.EventDelivery),
		BotTokenStatus: keyStatusOf(c.BotToken), PushSecretStatus: keyStatusOf(c.PushSecret), CallbackUrl: &callback,
		Proxy:            proxyOf(c.Proxy, c.ProxyPassword),
		Limiter:          gen.Limiter{Limit: int(c.Limiter.Limit), PerSeconds: int(c.Limiter.PerSeconds)},
		DestinationCount: int(c.DestinationCount), CreatedAt: c.CreatedAt, Etag: &tag}
	info := bridgeInfoOf(c.BridgeInfo)
	b.Info = info
	if c.BridgeInfoReadAt != nil && !info.IsNull() {
		b.InfoReadAt.Set(*c.BridgeInfoReadAt)
	} else {
		b.InfoReadAt.SetNull()
	}
	var out gen.Connection
	if err := out.FromBridgeConnection(b); err != nil {
		return gen.Connection{}, err
	}
	return out, nil
}

// bridgeInfoOf is a bridge's info as Muster stored it, in the API form: the fields of BridgeInfo; null without one,
// and for a copy that does not decode, which the next read replaces, as connections.StoredInfo treats it.
func bridgeInfoOf(raw json.RawMessage) nullable.Nullable[gen.BridgeInfo] {
	var out nullable.Nullable[gen.BridgeInfo]
	var i gen.BridgeInfo
	if len(raw) == 0 || json.Unmarshal(raw, &i) != nil {
		out.SetNull()
		return out
	}
	out.Set(i)
	return out
}

// slackInputOf is the Input of a Slack Connection's body: each of its three secrets replaces the stored one when sent.
func slackInputOf(body gen.ConnectionInput) (connections.Input, error) {
	sl, err := body.AsSlackConnectionInput()
	if err != nil {
		return connections.Input{}, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
			"The body is not a Slack Connection.")
	}
	in := connections.Input{Type: connections.TypeSlack, Name: sl.Name, EventMode: string(sl.EventMode),
		Proxy:   proxyInput(sl.Proxy),
		Limiter: connections.Limiter{Limit: int64(sl.Limiter.Limit), PerSeconds: int64(sl.Limiter.PerSeconds)}}
	if sl.BotToken != nil {
		in.BotToken = keyring.Replace(logging.Secret(*sl.BotToken))
	}
	if sl.AppToken != nil {
		in.AppToken = keyring.Replace(logging.Secret(*sl.AppToken))
	}
	if sl.SlackSigningSecret != nil {
		in.SlackSigningSecret = keyring.Replace(logging.Secret(*sl.SlackSigningSecret))
	}
	return in, nil
}

// slackConnectionOf is the API form of a Slack Connection: its three secrets as their status, its callback address,
// the bot, the workspace and the app its last passing check learned, null before one and after the bot token
// changed, and what the last hello of its Socket Mode connection counted.
func (s *Server) slackConnectionOf(c connections.Connection) (gen.Connection, error) {
	tag := etag(c.Version)
	callback := s.connections.SlackCallbackURL(c.PublicID)
	sl := gen.SlackConnection{Id: c.PublicID, Type: gen.SlackConnectionTypeSlack, Name: c.Name,
		EventMode: gen.SlackEventMode(c.EventMode), BotTokenStatus: keyStatusOf(c.BotToken),
		AppTokenStatus: keyStatusOf(c.AppToken), SlackSigningSecretStatus: keyStatusOf(c.SlackSigningSecret),
		CallbackUrl: &callback, BotName: nullableString(c.BotUsername), BotUserId: nullableString(c.BotUserID),
		TeamId: nullableString(c.SlackTeamID), TeamName: nullableString(c.SlackTeamName),
		AppId: nullableString(c.SlackAppID), Socket: slackSocketOf(c.SlackSocket),
		Proxy: proxyOf(c.Proxy, c.ProxyPassword), DestinationCount: int(c.DestinationCount), CreatedAt: c.CreatedAt,
		Limiter: gen.Limiter{Limit: int(c.Limiter.Limit), PerSeconds: int(c.Limiter.PerSeconds)}, Etag: &tag}
	var out gen.Connection
	if err := out.FromSlackConnection(sl); err != nil {
		return gen.Connection{}, err
	}
	return out, nil
}

// slackSocketOf is what the last hello of a Slack Connection's Socket Mode connection counted, in the API form; null in
// http mode and before the first hello after a save.
func slackSocketOf(sock *connections.SlackSocket) nullable.Nullable[gen.SlackSocketStatus] {
	var out nullable.Nullable[gen.SlackSocketStatus]
	if sock == nil {
		out.SetNull()
		return out
	}
	out.Set(gen.SlackSocketStatus{HelloAt: sock.HelloAt.UTC(), OtherConnections: sock.OtherConnections})
	return out
}

// slackCheckInfoOf is what a check of a Slack Connection found, in the API form: the workspace, the bot's user id and
// the app, null without the step app_token; null when the step token failed and for the other types.
func slackCheckInfoOf(id *connections.SlackIdentity) nullable.Nullable[gen.SlackCheckInfo] {
	var out nullable.Nullable[gen.SlackCheckInfo]
	if id == nil {
		out.SetNull()
		return out
	}
	info := gen.SlackCheckInfo{TeamId: id.TeamID, TeamName: id.TeamName, BotUserId: id.BotUserID}
	if id.AppID != "" {
		info.AppId.Set(id.AppID)
	} else {
		info.AppId.SetNull()
	}
	out.Set(info)
	return out
}
