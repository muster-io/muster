// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/publicid"
)

const (
	// codeAccountLinkedElsewhere refuses linking a messenger account linked to another User (C-18.FR-5).
	codeAccountLinkedElsewhere = "account_linked_elsewhere"
	// codeLinkCodeRequestsExceeded refuses a Mattermost, Bridge or Slack link code beyond
	// account_link.mattermost_code_requests, account_link.bridge_code_requests or account_link.slack_code_requests.
	codeLinkCodeRequestsExceeded = "link_code_requests_exceeded"
	// codeCodeNotOffered refuses a Bridge link code through a Connection whose info offers none (C-22.FR-15).
	codeCodeNotOffered = "code_not_offered"
	// codeMessagesTabDisabled refuses a Slack link code that Slack would not send because the Messages tab of the
	// Slack app is off (C-24.FR-12).
	codeMessagesTabDisabled = "messages_tab_disabled"
)

// AccountLinks is what the API needs of internal/accountlinks.
type AccountLinks interface {
	List(ctx context.Context, userID int64) ([]accountlinks.Link, error)
	ListOfUser(ctx context.Context, userPublicID string) ([]accountlinks.Link, error)
	Connections(ctx context.Context) ([]accountlinks.LinkableConnection, error)
	Unlink(ctx context.Context, r accountlinks.Requester, linkPublicID string) error
	UnlinkOfUser(ctx context.Context, r accountlinks.Requester, userPublicID, linkPublicID string) error
	StartTelegram(ctx context.Context, r accountlinks.Requester, connectionPublicID string) (
		accountlinks.TelegramStart, error)
	StartMattermost(ctx context.Context, r accountlinks.Requester, connectionPublicID, username string) (
		accountlinks.MattermostStarted, error)
	ConfirmMattermost(ctx context.Context, r accountlinks.Requester, requestPublicID, code string) (
		accountlinks.Link, error)
	StartBridge(ctx context.Context, r accountlinks.Requester, connectionPublicID string) (accountlinks.BridgeStart,
		error)
	StartBridgeCode(ctx context.Context, r accountlinks.Requester, connectionPublicID, handle string) (
		accountlinks.BridgeCodeStarted, error)
	ConfirmBridge(ctx context.Context, r accountlinks.Requester, requestPublicID, code string) (accountlinks.Link,
		error)
	StartSlack(ctx context.Context, r accountlinks.Requester, connectionPublicID, handle string) (
		accountlinks.SlackStarted, error)
	ConfirmSlack(ctx context.Context, r accountlinks.Requester, requestPublicID, code string) (accountlinks.Link,
		error)
}

// linkRequester is who asks through the API: the signed-in User — the owner of a Personal access token — whose own
// links change, with the actor, the Transport and the client address of the Audit log entry. A Service account never
// gets here for its own links: the middleware refuses it under /me.
func linkRequester(ctx context.Context) (accountlinks.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return accountlinks.Requester{}, err
	}
	return accountlinks.Requester{UserID: id.Session.User.ID, UserPublicID: id.Session.User.PublicID,
		Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// linkProblem maps the errors of internal/accountlinks to their problems; any other error is returned as it is.
func linkProblem(err error) error {
	if f, ok := errors.AsType[*accountlinks.FieldError](err); ok {
		p := fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
		p.AttemptsRemaining = f.AttemptsRemaining
		return p
	}
	if l, ok := errors.AsType[*accountlinks.RateLimitedError](err); ok {
		p := problem(http.StatusTooManyRequests, typeRateLimited, codeLinkCodeRequestsExceeded,
			"Too many link codes were requested within an hour; wait before the next one.")
		p.RetryAfter = l.Seconds()
		return p
	}
	if _, ok := errors.AsType[*accountlinks.MessengerError](err); ok {
		return problem(http.StatusConflict, typeConflict, "",
			"The messenger refused the call; check the Connection and try again.")
	}
	switch {
	case errors.Is(err, accountlinks.ErrNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such Account link or link request.")
	case errors.Is(err, accountlinks.ErrUserNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such user.")
	case errors.Is(err, accountlinks.ErrLinkedElsewhere):
		return problem(http.StatusConflict, typeConflict, codeAccountLinkedElsewhere,
			"This account is linked to another user. Its owner or an admin can unlink it.")
	case errors.Is(err, accountlinks.ErrCodeNotOffered):
		return problem(http.StatusConflict, typeConflict, codeCodeNotOffered,
			"This Bridge Connection offers no link code; send the token to the bot instead.")
	case errors.Is(err, accountlinks.ErrMessagesTabDisabled):
		return problem(http.StatusConflict, typeConflict, codeMessagesTabDisabled,
			"The Messages tab of the Slack app is off, so the bot cannot send the code; an admin of the Slack app "+
				"turns it on under App Home.")
	}
	return err
}

// ListMyAccountLinks is listMyAccountLinks: the caller's links with their messenger, Connection, username and last use
// (C-18.FR-10).
func (s *Server) ListMyAccountLinks(ctx context.Context, _ gen.ListMyAccountLinksRequestObject) (
	gen.ListMyAccountLinksResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	links, err := s.accountLinks.List(ctx, r.UserID)
	if err != nil {
		return nil, err
	}
	return gen.ListMyAccountLinks200JSONResponse(accountLinkListOf(links)), nil
}

// ListLinkableConnections is listLinkableConnections: the Connections that are not deleted, Slack ones included, with
// only their id, name and messenger, for any signed-in User (C-18.FR-10, C-24.FR-12); a Bridge Connection also has its
// link methods and the kind of handle a code needs, null when it offers none (C-22.FR-15), and no other Connection has
// either member.
func (s *Server) ListLinkableConnections(ctx context.Context, _ gen.ListLinkableConnectionsRequestObject) (
	gen.ListLinkableConnectionsResponseObject, error) {
	conns, err := s.accountLinks.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.LinkableConnectionList{Items: make([]gen.LinkableConnection, len(conns))}
	for i, c := range conns {
		out.Items[i] = gen.LinkableConnection{Id: c.PublicID, Name: c.Name, Messenger: gen.Messenger(c.Messenger)}
		if c.Messenger != accountlinks.MessengerBridge {
			continue
		}
		methods := make([]gen.LinkableConnectionLinkMethods, len(c.LinkMethods))
		for j, m := range c.LinkMethods {
			methods[j] = gen.LinkableConnectionLinkMethods(m)
		}
		out.Items[i].LinkMethods = &methods
		if c.HandleKind != "" {
			out.Items[i].HandleKind.Set(gen.LinkHandleKind(c.HandleKind))
		} else {
			out.Items[i].HandleKind.SetNull()
		}
	}
	return gen.ListLinkableConnections200JSONResponse(out), nil
}

// DeleteMyAccountLink is deleteMyAccountLink: the caller unlinks one of their accounts from the web session
// (C-18.FR-6); the middleware lets only the web session through.
func (s *Server) DeleteMyAccountLink(ctx context.Context, req gen.DeleteMyAccountLinkRequestObject) (
	gen.DeleteMyAccountLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.accountLinks.Unlink(ctx, r, req.AccountLinkId); err != nil {
		return nil, linkProblem(err)
	}
	return gen.DeleteMyAccountLink204Response{}, nil
}

// StartTelegramLink is startTelegramLink: the deep link of a new Telegram link of the caller (C-18.FR-2).
func (s *Server) StartTelegramLink(ctx context.Context, req gen.StartTelegramLinkRequestObject) (
	gen.StartTelegramLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	start, err := s.accountLinks.StartTelegram(ctx, r, req.Body.ConnectionId)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.StartTelegramLink201JSONResponse(gen.TelegramLinkStart{DeepLink: start.DeepLink,
		ExpiresAt: start.ExpiresAt.UTC()}), nil
}

// StartMattermostLink is startMattermostLink: the bot sends a code to the caller's Mattermost account (C-18.FR-3).
func (s *Server) StartMattermostLink(ctx context.Context, req gen.StartMattermostLinkRequestObject) (
	gen.StartMattermostLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	started, err := s.accountLinks.StartMattermost(ctx, r, req.Body.ConnectionId, req.Body.Username)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.StartMattermostLink202JSONResponse(gen.MattermostLinkStarted{RequestId: started.RequestPublicID,
		ExpiresAt: started.ExpiresAt.UTC(), AttemptsRemaining: started.AttemptsRemaining}), nil
}

// ConfirmMattermostLink is confirmMattermostLink: the code from the bot's direct message links the caller's
// Mattermost account (C-18.FR-3, AC-2).
func (s *Server) ConfirmMattermostLink(ctx context.Context, req gen.ConfirmMattermostLinkRequestObject) (
	gen.ConfirmMattermostLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	code := ""
	if req.Body.Code != nil {
		code = *req.Body.Code
	}
	l, err := s.accountLinks.ConfirmMattermost(ctx, r, req.Body.RequestId, code)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.ConfirmMattermostLink201JSONResponse(accountLinkOf(l)), nil
}

// StartBridgeLink is startBridgeLink: the token of a new link of the caller through a Bridge Connection, to send to
// its bot, and the link that hands it over when the bridge has one (C-22.FR-15).
func (s *Server) StartBridgeLink(ctx context.Context, req gen.StartBridgeLinkRequestObject) (
	gen.StartBridgeLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	start, err := s.accountLinks.StartBridge(ctx, r, req.Body.ConnectionId)
	if err != nil {
		return nil, linkProblem(err)
	}
	out := gen.BridgeLinkStart{Token: start.Token, ExpiresAt: start.ExpiresAt.UTC()}
	if start.LinkURL != nil {
		out.LinkUrl.Set(*start.LinkURL)
	} else {
		out.LinkUrl.SetNull()
	}
	return gen.StartBridgeLink201JSONResponse(out), nil
}

// StartBridgeCodeLink is startBridgeCodeLink: the bot of a Bridge Connection sends a code to the account the caller
// names by its handle (C-22.FR-15).
func (s *Server) StartBridgeCodeLink(ctx context.Context, req gen.StartBridgeCodeLinkRequestObject) (
	gen.StartBridgeCodeLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	started, err := s.accountLinks.StartBridgeCode(ctx, r, req.Body.ConnectionId, req.Body.Handle)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.StartBridgeCodeLink202JSONResponse(gen.BridgeCodeLinkStarted{RequestId: started.RequestPublicID,
		ExpiresAt: started.ExpiresAt.UTC(), AttemptsRemaining: started.AttemptsRemaining}), nil
}

// ConfirmBridgeLink is confirmBridgeLink: the code from the bot's direct message links the caller's account of the
// Bridge Connection (C-22.FR-15, AC-19).
func (s *Server) ConfirmBridgeLink(ctx context.Context, req gen.ConfirmBridgeLinkRequestObject) (
	gen.ConfirmBridgeLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	code := ""
	if req.Body.Code != nil {
		code = *req.Body.Code
	}
	l, err := s.accountLinks.ConfirmBridge(ctx, r, req.Body.RequestId, code)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.ConfirmBridgeLink201JSONResponse(accountLinkOf(l)), nil
}

// StartSlackLink is startSlackLink: the bot of a Slack Connection sends a code by direct message to the account the
// caller names by email or name (C-24.FR-12).
func (s *Server) StartSlackLink(ctx context.Context, req gen.StartSlackLinkRequestObject) (
	gen.StartSlackLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	started, err := s.accountLinks.StartSlack(ctx, r, req.Body.ConnectionId, req.Body.Handle)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.StartSlackLink202JSONResponse(gen.SlackLinkStarted{RequestId: started.RequestPublicID,
		ExpiresAt: started.ExpiresAt.UTC(), AttemptsRemaining: started.AttemptsRemaining}), nil
}

// ConfirmSlackLink is confirmSlackLink: the code from the bot's direct message links the caller's account of the
// Slack Connection (C-24.FR-12, AC-14).
func (s *Server) ConfirmSlackLink(ctx context.Context, req gen.ConfirmSlackLinkRequestObject) (
	gen.ConfirmSlackLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	code := ""
	if req.Body.Code != nil {
		code = *req.Body.Code
	}
	l, err := s.accountLinks.ConfirmSlack(ctx, r, req.Body.RequestId, code)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.ConfirmSlackLink201JSONResponse(accountLinkOf(l)), nil
}

// ListUserAccountLinks is listUserAccountLinks: the links of a User, for users:read (C-18.FR-6).
func (s *Server) ListUserAccountLinks(ctx context.Context, req gen.ListUserAccountLinksRequestObject) (
	gen.ListUserAccountLinksResponseObject, error) {
	links, err := s.accountLinks.ListOfUser(ctx, req.UserId)
	if err != nil {
		return nil, linkProblem(err)
	}
	return gen.ListUserAccountLinks200JSONResponse(accountLinkListOf(links)), nil
}

// DeleteUserAccountLink is deleteUserAccountLink: an Admin removes a link of a User; no operation creates one for
// someone else (C-18.FR-6, AC-4). An Admin's own link is removed from the web session only, as under /me: with a token
// it is 403 session_required (C-03.AC-14).
func (s *Server) DeleteUserAccountLink(ctx context.Context, req gen.DeleteUserAccountLinkRequestObject) (
	gen.DeleteUserAccountLinkResponseObject, error) {
	r, err := linkRequester(ctx)
	if err != nil {
		return nil, err
	}
	if id, _ := identity(ctx); id.Token != nil && r.UserPublicID != "" {
		if norm, err := publicid.Parse(publicid.User, req.UserId); err == nil && norm == r.UserPublicID {
			return nil, errSessionRequired
		}
	}
	if err := s.accountLinks.UnlinkOfUser(ctx, r, req.UserId, req.AccountLinkId); err != nil {
		return nil, linkProblem(err)
	}
	return gen.DeleteUserAccountLink204Response{}, nil
}

func accountLinkListOf(links []accountlinks.Link) gen.AccountLinkList {
	out := gen.AccountLinkList{Items: make([]gen.AccountLink, len(links))}
	for i, l := range links {
		out.Items[i] = accountLinkOf(l)
	}
	return out
}

func accountLinkOf(l accountlinks.Link) gen.AccountLink {
	out := gen.AccountLink{Id: l.PublicID, Messenger: gen.Messenger(l.Messenger), ExternalId: l.ExternalID,
		CreatedAt: l.CreatedAt.UTC(), LastUsedAt: nullableTime(l.LastUsedAt),
		Connection: &gen.EntityRef{Id: l.ConnectionPublicID, Name: l.ConnectionName}, DisplayName: l.DisplayName}
	if l.Username != nil {
		out.Username.Set(*l.Username)
	} else {
		out.Username.SetNull()
	}
	return out
}
