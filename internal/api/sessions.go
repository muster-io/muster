// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
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

// GetSignInOptions is getSignInOptions: public, and says only whether the OIDC button shows. OIDC arrives with C-03's
// OIDC story; until then it is off.
func (s *Server) GetSignInOptions(context.Context, gen.GetSignInOptionsRequestObject) (
	gen.GetSignInOptionsResponseObject, error) {
	var opts gen.SignInOptions
	opts.Oidc.Enabled = false
	return gen.GetSignInOptions200JSONResponse(opts), nil
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
