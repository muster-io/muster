// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package api is the HTTP API of the app listener (ADR-0008): the strict server generated from api/openapi.yaml,
// mounted under /api/v1, behind the middleware of the contract — request metrics, authentication by the session
// cookie, the CSRF check, request validation and the Permission of each operation's x-permission — with every error
// answered as an RFC 9457 problem. Handlers are thin: they call the domain packages. An operation that no story has
// implemented yet answers 501.
package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	spec "github.com/muster-io/muster/api"
	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/users"
)

// BasePath is where the API is mounted on the app listener.
const BasePath = "/api/v1"

// Sessions is what the API needs of internal/auth.
type Sessions interface {
	SignIn(ctx context.Context, req auth.SignInRequest) (auth.Session, error)
	Authenticate(ctx context.Context, cookie string) (auth.Session, error)
	Permissions(sess auth.Session) []auth.Permission
	CSRFToken(sess auth.Session) (string, error)
	CheckCSRF(sess auth.Session, header string) bool
	SignOut(ctx context.Context, sess auth.Session, addr netip.Addr) error
	SignOutEverywhere(ctx context.Context, sess auth.Session, addr netip.Addr) error
	ListSessions(ctx context.Context, sess auth.Session) ([]auth.SessionInfo, error)
	ChangePassword(ctx context.Context, sess auth.Session, c auth.PasswordChange) error
	Roles() auth.Roles
}

// Users is what the API needs of internal/users.
type Users interface {
	Get(ctx context.Context, id int64) (users.User, error)
	UpdateProfile(ctx context.Context, id int64, actor audit.Actor, p users.Profile, addr netip.Addr) (users.User, error)
}

// Config is what the API serves with.
type Config struct {
	Sessions Sessions
	Users    Users
	// TrustedProxies are MUSTER_TRUSTED_PROXIES, for the client address.
	TrustedProxies []netip.Prefix
	Log            *logging.Logger
	// Real is the real clock, for the request durations.
	Real clock.Clock
}

// Server is the API handler.
type Server struct {
	gen.StrictServerInterface // nil: the operations no story has implemented answer 501 before reaching it

	sessions       Sessions
	users          Users
	trustedProxies []netip.Prefix
	log            *logging.Logger
	real           clock.Clock

	router     routers.Router
	operations map[*openapi3.Operation]*operation
	validated  http.Handler
}

// implemented are the operations this build serves, by the method names of the strict server.
var implemented = map[string]bool{
	"GetSignInOptions": true, "CreateSession": true, "GetCurrentSession": true, "DeleteCurrentSession": true,
	"GetMe": true, "UpdateMe": true, "ChangePassword": true, "ListMySessions": true, "DeleteMySessions": true,
	"ListRoles": true, "GetOpenApiSpec": true,
}

// LoadSpec parses the embedded specification with the app listener's base path as its only server, which is how
// the router and the request validation match request paths.
func LoadSpec() (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(spec.Spec())
	if err != nil {
		return nil, fmt.Errorf("load the API specification: %w", err)
	}
	doc.Servers = openapi3.Servers{{URL: BasePath}}
	return doc, nil
}

// New returns the API handler; it serves every path under BasePath.
func New(cfg Config) (*Server, error) {
	doc, err := LoadSpec()
	if err != nil {
		return nil, err
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("route the API specification: %w", err)
	}
	s := &Server{
		sessions: cfg.Sessions, users: cfg.Users, trustedProxies: cfg.TrustedProxies, log: cfg.Log, real: cfg.Real,
		router: router, operations: readOperations(doc),
	}
	mux := http.NewServeMux()
	strict := gen.NewStrictHandlerWithOptions(s, []gen.StrictMiddlewareFunc{notImplemented},
		gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
				writeProblem(w, r, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
					"The request body is not valid JSON of the expected type."))
			},
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				op := infoFrom(r.Context()).operation
				id := ""
				if op != nil {
					id = op.id
				}
				writeProblem(w, r, s.problemFor(r.Context(), id, err))
			},
		})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseURL:    BasePath,
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, r, fieldProblem(http.StatusBadRequest, "", fieldInvalidFormat,
				"A parameter is not valid."))
		},
	})
	s.validated = s.validator(doc)(s.permit(mux))
	return s, nil
}

// notImplemented answers 501 for every operation this build does not implement yet, before its handler runs.
func notImplemented(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if implemented[operationID] {
		return f
	}
	return func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
		return nil, errNotImplemented
	}
}

// GetOpenApiSpec is getOpenApiSpec: the embedded specification, byte for byte (C-03.FR-23).
//
//nolint:revive // var-naming: the name comes from the generated interface
func (s *Server) GetOpenApiSpec(context.Context, gen.GetOpenApiSpecRequestObject) (
	gen.GetOpenApiSpecResponseObject, error) {
	b := spec.Spec()
	return gen.GetOpenApiSpec200ApplicationyamlResponse{Body: bytes.NewReader(b), ContentLength: int64(len(b))}, nil
}

// identity is the identity the middleware attached; operations that need one never run without it.
func identity(ctx context.Context) (*auth.Identity, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	return id, nil
}

// userOf is the API form of a user.
func userOf(u users.User) gen.User {
	etag := u.ETag()
	method := gen.UserSignInMethod(u.SignInMethod())
	offline := u.OfflineAccess
	out := gen.User{
		Id: u.PublicID, Name: u.Name, Login: u.Login, Role: gen.RoleName(u.Role), Source: gen.UserSource(u.Source),
		Status: gen.UserStatus(u.Status), TotpEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt.UTC(), Etag: &etag,
		SignInMethod: &method,
	}
	if u.HasOIDCIdentity {
		out.OidcOfflineAccess = &offline
	}
	if u.Email != "" {
		out.Email.Set(u.Email)
	} else {
		out.Email.SetNull()
	}
	if u.TimeZone != nil {
		out.TimeZone.Set(*u.TimeZone)
	} else {
		out.TimeZone.SetNull()
	}
	if u.Language != nil {
		out.Language.Set(gen.NullableLanguage(*u.Language))
	} else {
		out.Language.SetNull()
	}
	if u.LastSignInAt != nil {
		out.LastSignInAt.Set(u.LastSignInAt.UTC())
	} else {
		out.LastSignInAt.SetNull()
	}
	return out
}

func permissionsOf(perms []auth.Permission) []gen.Permission {
	out := make([]gen.Permission, len(perms))
	for i, p := range perms {
		out[i] = gen.Permission(p)
	}
	return out
}
