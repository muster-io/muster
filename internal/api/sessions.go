// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc"
)

// sessionCookie carries a session: HttpOnly, Secure and SameSite=Lax on the whole site, ending with the browser
// session; the session itself ends on the server (C-03.FR-9).
func sessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: auth.CookieName, Value: value, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode}
}

// clearedCookie removes the session cookie from the browser.
func clearedCookie() *http.Cookie {
	c := sessionCookie("") //nolint:gosec // G124: the cookie is HttpOnly, Secure and SameSite=Lax; the linter misses the helper
	c.MaxAge = -1
	return c
}

// oidcStateCookie binds an OIDC callback to the browser that started the sign-in: it carries the state of the start.
const oidcStateCookie = "muster_oidc_state"

// oidcStateCookieOf is the binding cookie with value, valid as long as the request it binds; an empty value removes
// it. SameSite=Lax lets the browser send it on the top-level redirect back from the identity provider.
func oidcStateCookieOf(value string) *http.Cookie {
	c := &http.Cookie{Name: oidcStateCookie, Value: value, Path: BasePath + "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(oidc.AuthRequestTTL / time.Second)}
	if value == "" {
		c.MaxAge = -1
	}
	return c
}

type oidcStateKey struct{}

// withOIDCState is a strict middleware that hands the binding cookie of the request to completeOidcSignIn, whose
// generated request object carries no cookies.
func withOIDCState(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if operationID != "CompleteOidcSignIn" {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		if c, err := r.Cookie(oidcStateCookie); err == nil {
			ctx = context.WithValue(ctx, oidcStateKey{}, c.Value)
		}
		return f(ctx, w, r, request)
	}
}

// GetSignInOptions is getSignInOptions: public, and says only whether the OIDC button shows and its provider name
// (C-03.FR-24).
func (s *Server) GetSignInOptions(ctx context.Context, _ gen.GetSignInOptionsRequestObject) (
	gen.GetSignInOptionsResponseObject, error) {
	var opts gen.SignInOptions
	if s.oidc == nil {
		return gen.GetSignInOptions200JSONResponse(opts), nil
	}
	enabled, name, err := s.oidc.SignInOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts.Oidc.Enabled = enabled
	if enabled {
		opts.Oidc.DisplayName = &name
	}
	return gen.GetSignInOptions200JSONResponse(opts), nil
}

// redirect answers 302 with Location and cookies, for the OIDC redirect flow.
type redirect struct {
	location string
	cookies  []*http.Cookie
}

func (r redirect) write(w http.ResponseWriter) error {
	for _, c := range r.cookies {
		http.SetCookie(w, c)
	}
	w.Header().Set("Location", r.location)
	w.WriteHeader(http.StatusFound)
	return nil
}

func (r redirect) VisitStartOidcSignInResponse(w http.ResponseWriter) error { return r.write(w) }

func (r redirect) VisitCompleteOidcSignInResponse(w http.ResponseWriter) error { return r.write(w) }

// StartOidcSignIn is startOidcSignIn: the redirect to the identity provider with PKCE S256, state and nonce
// (C-03.FR-25), and the cookie that binds the callback to this browser. return_to is kept only when it is a relative
// path. A failed discovery sends the browser to the sign-in page with idp_error.
func (s *Server) StartOidcSignIn(ctx context.Context, req gen.StartOidcSignInRequestObject) (
	gen.StartOidcSignInResponseObject, error) {
	if s.oidc == nil {
		return nil, errOIDCNotEnabled
	}
	start, err := s.oidc.StartSignIn(ctx, deref(req.Params.ReturnTo))
	if be, ok := errors.AsType[*oidc.BackChannelError](err); ok {
		s.log.Log(ctx, logging.OIDCSignInFailed, logging.F("reason", be.Step), logging.F("error", be.Error()))
		return redirect{location: oidc.SignInPage + "?error=" + oidc.ErrorIDPError}, nil
	}
	if err != nil {
		return nil, err
	}
	return redirect{location: start.URL, cookies: []*http.Cookie{oidcStateCookieOf(start.State)}}, nil
}

// CompleteOidcSignIn is completeOidcSignIn: every outcome is a redirect to the SPA — the page of return_to or / with
// the session cookie, or the sign-in page with the error (C-03.FR-25). The binding cookie is removed either way.
func (s *Server) CompleteOidcSignIn(ctx context.Context, req gen.CompleteOidcSignInRequestObject) (
	gen.CompleteOidcSignInResponseObject, error) {
	cookies := []*http.Cookie{oidcStateCookieOf("")}
	if s.oidc == nil {
		return redirect{location: oidc.SignInPage + "?error=" + oidc.ErrorOIDCDisabled, cookies: cookies}, nil
	}
	state, _ := ctx.Value(oidcStateKey{}).(string)
	out := s.oidc.CompleteSignIn(ctx, oidc.Callback{
		Code: deref(req.Params.Code), State: deref(req.Params.State), Error: deref(req.Params.Error),
		CookieState: state, Address: clientAddress(ctx), UserAgent: infoFrom(ctx).userAgent,
	})
	if out.Session != nil {
		cookies = append(cookies, sessionCookie(out.Session.Cookie()))
	}
	return redirect{location: out.Redirect, cookies: cookies}, nil
}

// sessionCreated answers createSession with the session cookie.
type sessionCreated struct {
	cookie *http.Cookie
	body   gen.Session
}

func (r sessionCreated) VisitCreateSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	return gen.CreateSession201JSONResponse(r.body).VisitCreateSessionResponse(w)
}

// CreateSession is createSession: local sign-in (C-03.FR-3, FR-24), with the second factor when the request carries
// it; a user with TOTP who gives no code gets a session in the state totp_required.
func (s *Server) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (
	gen.CreateSessionResponseObject, error) {
	if req.Body == nil || req.Body.Password == nil {
		return nil, fieldProblem(http.StatusBadRequest, "/password", fieldRequired, "The password is missing.")
	}
	sess, err := s.sessions.SignIn(ctx, auth.SignInRequest{
		Login: req.Body.Login, Password: *req.Body.Password, Address: clientAddress(ctx),
		UserAgent: infoFrom(ctx).userAgent,
		Proof:     auth.Proof{TOTPCode: deref(req.Body.TotpCode), RecoveryCode: deref(req.Body.RecoveryCode)},
	})
	if err != nil {
		return nil, err
	}
	body, err := s.sessionOf(ctx, sess)
	if err != nil {
		return nil, err
	}
	return sessionCreated{cookie: sessionCookie(sess.Cookie()), body: body}, nil
}

// GetCurrentSession is getCurrentSession: the session in any state, with its CSRF token.
func (s *Server) GetCurrentSession(ctx context.Context, _ gen.GetCurrentSessionRequestObject) (
	gen.GetCurrentSessionResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	body, err := s.sessionOf(ctx, id.Session)
	if err != nil {
		return nil, err
	}
	return gen.GetCurrentSession200JSONResponse(body), nil
}

// SubmitSessionTotp is submitSessionTotp: a TOTP code or a recovery code completes a session in the state
// totp_required (C-03.FR-24, AC-13).
func (s *Server) SubmitSessionTotp(ctx context.Context, req gen.SubmitSessionTotpRequestObject) (
	gen.SubmitSessionTotpResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	p := auth.Proof{TOTPCode: deref(req.Body.TotpCode), RecoveryCode: deref(req.Body.RecoveryCode)}
	if !p.Given() {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "Give a TOTP code or a recovery code.")
	}
	sess, err := s.sessions.SubmitSecondFactor(ctx, id.Session, p, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	body, err := s.sessionOf(ctx, sess)
	if err != nil {
		return nil, err
	}
	return gen.SubmitSessionTotp200JSONResponse(body), nil
}

// signedOut answers with no content and removes the session cookie.
type signedOut struct{}

func (signedOut) VisitDeleteCurrentSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, clearedCookie())
	return gen.DeleteCurrentSession204Response{}.VisitDeleteCurrentSessionResponse(w)
}

func (signedOut) VisitDeleteMySessionsResponse(w http.ResponseWriter) error {
	http.SetCookie(w, clearedCookie())
	return gen.DeleteMySessions204Response{}.VisitDeleteMySessionsResponse(w)
}

// DeleteCurrentSession is deleteCurrentSession: sign out.
func (s *Server) DeleteCurrentSession(ctx context.Context, _ gen.DeleteCurrentSessionRequestObject) (
	gen.DeleteCurrentSessionResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.SignOut(ctx, id.Session, clientAddress(ctx)); err != nil {
		return nil, err
	}
	return signedOut{}, nil
}

// sessionOf is the API form of a session, with its user, CSRF token and Permissions.
func (s *Server) sessionOf(ctx context.Context, sess auth.Session) (gen.Session, error) {
	u, err := s.self(ctx, sess.User.ID)
	if err != nil {
		return gen.Session{}, err
	}
	token, err := s.sessions.CSRFToken(sess)
	if err != nil {
		return gen.Session{}, err
	}
	return gen.Session{
		User: userOf(u), State: gen.SessionState(sess.State), CsrfToken: token, ExpiresAt: sess.ExpiresAt.UTC(),
		IdleExpiresAt: sess.IdleExpiresAt.UTC(), Method: gen.SessionMethod(sess.Method),
		Permissions: permissionsOf(s.sessions.Permissions(sess)),
	}, nil
}

// CompletePasswordSetup is completePasswordSetup, public: it sets the password from the token of a setup link
// (C-03.FR-26).
func (s *Server) CompletePasswordSetup(ctx context.Context, req gen.CompletePasswordSetupRequestObject) (
	gen.CompletePasswordSetupResponseObject, error) {
	switch {
	case req.Body == nil:
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	case req.Body.Token == nil:
		return nil, fieldProblem(http.StatusBadRequest, "/token", fieldRequired, "The token is missing.")
	case req.Body.Password == nil:
		return nil, fieldProblem(http.StatusBadRequest, "/password", fieldRequired, "The password is missing.")
	}
	if err := s.admin.CompleteSetup(ctx, *req.Body.Token, *req.Body.Password, clientAddress(ctx)); err != nil {
		return nil, err
	}
	return gen.CompletePasswordSetup204Response{}, nil
}
