// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/users"
)

// GetMe is getMe: the caller and the effective Permissions (C-03.FR-12).
func (s *Server) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.self(ctx, id.Session.User.ID)
	if err != nil {
		return nil, err
	}
	return gen.GetMe200JSONResponse(gen.Me{User: userOf(u), Permissions: permissionsOf(id.Permissions)}), nil
}

// UpdateMe is updateMe: the name, the time zone and the language. An omitted time zone or language keeps its value
// and null clears it, so that it follows the browser.
func (s *Server) UpdateMe(ctx context.Context, req gen.UpdateMeRequestObject) (gen.UpdateMeResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	current, err := s.self(ctx, id.Session.User.ID)
	if err != nil {
		return nil, err
	}
	p := users.Profile{Name: req.Body.Name, TimeZone: current.TimeZone, Language: current.Language}
	if req.Body.TimeZone.IsSpecified() {
		p.TimeZone = nil
		if !req.Body.TimeZone.IsNull() {
			tz := req.Body.TimeZone.MustGet()
			p.TimeZone = &tz
		}
	}
	if req.Body.Language.IsSpecified() {
		p.Language = nil
		if !req.Body.Language.IsNull() {
			lang := string(req.Body.Language.MustGet())
			p.Language = &lang
		}
	}
	u, err := s.users.UpdateProfile(ctx, id.Session.User.ID, id.Actor(), p, clientAddress(ctx))
	if errors.Is(err, users.ErrNotFound) {
		return nil, errUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateMe200JSONResponse(gen.Me{User: userOf(u), Permissions: permissionsOf(id.Permissions)}), nil
}

// ChangePassword is changePassword: it needs the current password and ends the user's other sessions.
func (s *Server) ChangePassword(ctx context.Context, req gen.ChangePasswordRequestObject) (
	gen.ChangePasswordResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case req.Body == nil:
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	case req.Body.CurrentPassword == nil:
		return nil, fieldProblem(http.StatusBadRequest, "/current_password", fieldRequired,
			"The current password is missing.")
	case req.Body.NewPassword == nil:
		return nil, fieldProblem(http.StatusBadRequest, "/new_password", fieldRequired, "The new password is missing.")
	}
	if err := s.sessions.ChangePassword(ctx, id.Session, auth.PasswordChange{
		Current: *req.Body.CurrentPassword, New: *req.Body.NewPassword, Address: clientAddress(ctx),
	}); err != nil {
		return nil, err
	}
	return gen.ChangePassword204Response{}, nil
}

// ListMySessions is listMySessions: the user's sessions, the current one marked.
func (s *Server) ListMySessions(ctx context.Context, _ gen.ListMySessionsRequestObject) (
	gen.ListMySessionsResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.sessions.ListSessions(ctx, id.Session)
	if err != nil {
		return nil, err
	}
	items := make([]gen.SessionInfo, 0, len(list))
	for _, info := range list {
		item := gen.SessionInfo{
			Id: info.PublicID, Method: gen.SessionMethod(info.Method), CreatedAt: info.CreatedAt.UTC(),
			LastUsedAt: info.LastUsedAt.UTC(), Current: info.Current,
		}
		if info.Address.IsValid() {
			item.Address.Set(info.Address.String())
		} else {
			item.Address.SetNull()
		}
		if info.UserAgent != "" {
			item.UserAgent.Set(info.UserAgent)
		} else {
			item.UserAgent.SetNull()
		}
		items = append(items, item)
	}
	return gen.ListMySessions200JSONResponse(gen.SessionInfoList{Items: items}), nil
}

// DeleteMySessions is deleteMySessions: sign out everywhere, the current session included.
func (s *Server) DeleteMySessions(ctx context.Context, _ gen.DeleteMySessionsRequestObject) (
	gen.DeleteMySessionsResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.SignOutEverywhere(ctx, id.Session, clientAddress(ctx)); err != nil {
		return nil, err
	}
	return signedOut{}, nil
}

// self reads the caller's own user; a user who no longer exists has no valid session.
func (s *Server) self(ctx context.Context, id int64) (users.User, error) {
	u, err := s.users.Get(ctx, id)
	if errors.Is(err, users.ErrNotFound) {
		return users.User{}, errUnauthenticated
	}
	return u, err
}

// StartOidcLink is startOidcLink: the authorization URL of a link of the caller's account to an identity at the
// identity provider, bound to this web session (C-03.FR-29).
func (s *Server) StartOidcLink(ctx context.Context, _ gen.StartOidcLinkRequestObject) (
	gen.StartOidcLinkResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if s.oidc == nil {
		return nil, errOIDCNotEnabled
	}
	start, err := s.oidc.StartLink(ctx, id.Session)
	if be, ok := errors.AsType[*oidc.BackChannelError](err); ok {
		s.log.Log(ctx, logging.OIDCSignInFailed, logging.F("reason", "link "+be.Step),
			logging.F("error", be.Error()))
		return nil, problem(http.StatusInternalServerError, typeInternal, "",
			"The identity provider cannot be reached; try again later.")
	}
	if err != nil {
		return nil, err
	}
	return gen.StartOidcLink201JSONResponse(gen.OidcLinkStart{AuthorizationUrl: start.URL,
		ExpiresAt: start.ExpiresAt.UTC()}), nil
}

func (r redirect) VisitCompleteOidcLinkResponse(w http.ResponseWriter) error { return r.write(w) }

// CompleteOidcLink is completeOidcLink: the identity provider's redirect back to a link, accepted only in the web
// session that started it; every outcome is a redirect to the profile, with ?error=<code> after a failure.
func (s *Server) CompleteOidcLink(ctx context.Context, req gen.CompleteOidcLinkRequestObject) (
	gen.CompleteOidcLinkResponseObject, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || s.oidc == nil {
		return redirect{location: oidc.ProfilePage + "?error=" + oidc.ErrorInvalidRequest}, nil
	}
	out := s.oidc.CompleteLink(ctx, id.Session, oidc.Callback{
		Code: deref(req.Params.Code), State: deref(req.Params.State), Error: deref(req.Params.Error),
		Address: clientAddress(ctx), UserAgent: infoFrom(ctx).userAgent,
	})
	return redirect{location: out.Redirect}, nil
}
