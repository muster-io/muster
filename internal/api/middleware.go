// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc"
)

// MaxBodyBytes bounds the body of an API request on the app listener; a larger one is 413 payload-too-large.
const MaxBodyBytes = 1 << 20

// operation is what the middleware needs of an operation of the specification.
type operation struct {
	id   string
	path string
	// permissions are the values of x-permission: one Permission, alternatives, or a special value.
	permissions []string
	// dispatcherChecks are the Command operations marked x-permission-check: dispatcher, whose Permission the
	// dispatcher of internal/groups checks as its first step (ADR-0016); the middleware checks only authentication.
	dispatcherChecks bool
	// public operations need no credentials: security is empty.
	public bool
	// sessionOnly operations accept only the web session (C-03.FR-27).
	sessionOnly bool
	// validate is false for the ingestion operations, which take any body (ADR-0002).
	validate bool
}

// permissionCheckDispatcher is the value of x-permission-check that leaves the Permission to the dispatcher.
const permissionCheckDispatcher = "dispatcher"

// operationStreamLiveUpdates is the live-updates stream, which the request duration metric leaves out.
const operationStreamLiveUpdates = "streamLiveUpdates"

// operationCompleteOidcLink is the callback of a link, which answers every outcome with a redirect.
const operationCompleteOidcLink = "completeOidcLink"

// limitedOperations are the operations a limited session may call, per state (SessionState in the specification).
var limitedOperations = map[auth.SessionState][]string{
	auth.StateTOTPRequired:          {"getCurrentSession", "deleteCurrentSession", "submitSessionTotp"},
	auth.StateTOTPEnrolmentRequired: {"getCurrentSession", "deleteCurrentSession", "beginTotpEnrolment", "confirmTotpEnrolment", "getMyTotp"},
}

// safeMethod reports whether requests with method m change nothing and need no CSRF token.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// readOperations reads the operations of the app listener from the specification.
func readOperations(doc *openapi3.T) map[*openapi3.Operation]*operation {
	ops := map[*openapi3.Operation]*operation{}
	for path, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if ext, _ := op.Extensions["x-listener"].(string); ext != "app" {
				continue
			}
			o := &operation{id: op.OperationID, path: path, permissions: xPermission(op), validate: true}
			check, _ := op.Extensions["x-permission-check"].(string)
			o.dispatcherChecks = check == permissionCheckDispatcher
			if op.Security != nil {
				o.public = len(*op.Security) == 0
				o.sessionOnly = !o.public && slices.IndexFunc(*op.Security, func(req openapi3.SecurityRequirement) bool {
					for name := range req {
						if name != "sessionCookie" && name != "csrf" {
							return true
						}
					}
					return false
				}) < 0
			}
			ops[op] = o
		}
	}
	return ops
}

func xPermission(op *openapi3.Operation) []string {
	switch v := op.Extensions["x-permission"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, p := range v {
			if s, ok := p.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// requestInfo is what the handlers need of the request besides its parsed parameters.
type requestInfo struct {
	operation *operation
	userAgent string
}

type requestInfoKey struct{}

func infoFrom(ctx context.Context) requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(requestInfo)
	return info
}

// statusRecorder remembers the status of the answer for the metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ServeHTTP is the middleware chain of the API, in the order of the contract: security headers, request metrics,
// authentication, the CSRF check, request validation, the Permission of the operation, then the handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := s.real.Now()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	rec := &statusRecorder{ResponseWriter: w}
	op := s.route(r)
	defer func() {
		pattern := metrics.RouteUnmatched
		if op != nil {
			pattern = op.path
		}
		method := r.Method
		if !slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete, http.MethodOptions}, method) {
			method = metrics.MethodOther
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		metrics.APIRequests.With(pattern, method, strconv.Itoa(status)).Inc()
		// A live-updates stream lasts as long as its tab is open; its duration says nothing about the API's speed.
		if op == nil || op.id != operationStreamLiveUpdates || status != http.StatusOK {
			metrics.APIRequestDuration.With(pattern, method).Update(s.real.Now().Sub(start).Seconds())
		}
	}()
	if op == nil {
		writeProblem(rec, r, errNotFound)
		return
	}
	// A browser marks a request another site made; no mutating request of the UI or of automation comes cross-site,
	// which also keeps the public sign-in out of reach of login CSRF.
	if !safeMethod(r.Method) && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeProblem(rec, r, errCSRF)
		return
	}
	r.Body = http.MaxBytesReader(rec, r.Body, MaxBodyBytes)
	ctx := auth.WithClientAddress(r.Context(), auth.ClientAddress(r.RemoteAddr, r.Header.Values("X-Forwarded-For"),
		s.trustedProxies))
	ctx = context.WithValue(ctx, requestInfoKey{}, requestInfo{operation: op, userAgent: r.UserAgent()})
	r = r.WithContext(ctx)
	if !op.public {
		id, p := s.authenticate(rec, r, op)
		if p == nil {
			p = credentialsAllowed(id, op)
		}
		if p != nil && op.id == operationCompleteOidcLink {
			// The identity provider sends the browser back here: every outcome is a redirect to the profile, and a
			// link without a usable web session is refused like one from another session.
			rec.Header().Set("Location", oidc.ProfilePage+"?error="+oidc.ErrorInvalidRequest)
			rec.WriteHeader(http.StatusFound)
			return
		}
		if p != nil {
			writeProblem(rec, r, p)
			return
		}
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
	}
	s.validated.ServeHTTP(rec, r)
}

// route finds the operation of the app listener that serves r; nil when there is none.
func (s *Server) route(r *http.Request) *operation {
	if !strings.HasPrefix(r.URL.Path, BasePath+"/") {
		return nil
	}
	route, _, err := s.router.FindRoute(r)
	if err != nil {
		return nil
	}
	return s.operations[route.Operation]
}

// authenticate finds the identity of a request to an operation that needs one: a bearer token (C-04.FR-4), which needs
// no CSRF token, or else the web session of the cookie, with its CSRF token on a mutating request.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, op *operation) (*auth.Identity, *Problem) {
	if scheme, value, _ := strings.Cut(r.Header.Get("Authorization"), " "); strings.EqualFold(scheme, "bearer") {
		if s.tokens == nil {
			return nil, errUnauthenticated
		}
		id, err := s.tokens.Authenticate(r.Context(), strings.TrimSpace(value), clientAddress(r.Context()))
		if err != nil {
			return nil, s.problemFor(r.Context(), op.id, err)
		}
		return id, nil
	}
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil || cookie.Value == "" {
		return nil, errUnauthenticated
	}
	sess, err := s.sessions.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		p := s.problemFor(r.Context(), op.id, err)
		if p.Status == http.StatusUnauthorized {
			http.SetCookie(w, clearedCookie())
		}
		return nil, p
	}
	if sess.State != auth.StateActive && !slices.Contains(limitedOperations[sess.State], op.id) {
		code := codeTOTPRequired
		if sess.State == auth.StateTOTPEnrolmentRequired {
			code = codeTOTPEnrolmentRequired
		}
		return nil, problem(http.StatusForbidden, typeForbidden, code, "Finish the second factor first.")
	}
	if !safeMethod(r.Method) && !s.sessions.CheckCSRF(sess, r.Header.Get(auth.CSRFHeader)) {
		return nil, errCSRF
	}
	return &auth.Identity{Session: sess, Permissions: s.sessions.Permissions(sess), Transport: audit.TransportUI}, nil
}

// credentialsAllowed refuses a token where the operation needs the web session — the operations that change the
// caller's own account, tokens included (C-03.FR-27, C-04.FR-7) — and a Service account under /me, which it does not
// have. It runs before request validation, so that a token learns this whatever its request carries.
func credentialsAllowed(id *auth.Identity, op *operation) *Problem {
	if op.sessionOnly && id.Token != nil {
		return errSessionRequired
	}
	if id.IsServiceAccount() && (op.path == "/me" || strings.HasPrefix(op.path, "/me/")) {
		return errServiceAccountDenied
	}
	return nil
}

// permit checks the Permission of the operation's x-permission; it runs after request validation. A Command operation
// marked x-permission-check: dispatcher only needs an identity: the dispatcher checks its Permission.
func (s *Server) permit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := infoFrom(r.Context()).operation
		if op == nil || op.public {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeProblem(w, r, errUnauthenticated)
			return
		}
		if !op.dispatcherChecks && !allowed(id, op.permissions) {
			writeProblem(w, r, forbidden(op.permissions...))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowed reports whether id may call an operation with the x-permission values perms: any signed-in identity for
// authenticated, otherwise one of the listed Permissions.
func allowed(id *auth.Identity, perms []string) bool {
	for _, p := range perms {
		switch p {
		case auth.PermissionAuthenticated:
			return true
		case auth.PermissionNone, auth.PermissionIntegrationToken:
			continue
		}
		if id.Can(auth.Permission(p)) {
			return true
		}
	}
	return false
}

// validator is the request validation of nethttp-middleware over the specification; the ingestion operations are
// skipped.
func (s *Server) validator(doc *openapi3.T) func(http.Handler) http.Handler {
	return nethttpmiddleware.OapiRequestValidatorWithOptions(doc, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, // authentication ran before
			MultiError:         true,
		},
		SilenceServersWarning: true,
		Skipper: func(r *http.Request) bool {
			op := infoFrom(r.Context()).operation
			return op == nil || !op.validate
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, r *http.Request,
			opts nethttpmiddleware.ErrorHandlerOpts) {
			if opts.MatchedRoute == nil {
				writeProblem(w, r, errNotFound)
				return
			}
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				writeProblem(w, r, errTooLarge)
				return
			}
			writeProblem(w, r, validationProblem(err))
		},
	})
}

// clientAddress is the client address of the request, for the Audit log and the sign-in throttle.
func clientAddress(ctx context.Context) netip.Addr {
	return auth.ClientAddressFrom(ctx)
}
