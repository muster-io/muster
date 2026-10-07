// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package api is the HTTP API of the app listener (ADR-0008): the strict server generated from api/openapi.yaml,
// mounted under /api/v1, behind the middleware of the contract — request metrics, authentication by a bearer token or
// the session cookie, the CSRF check of the session, request validation and the Permission of each operation's
// x-permission — with every error answered as an RFC 9457 problem. Handlers are thin: they call the domain packages.
// An operation that no story has implemented yet answers 501.
package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/tokens"
	"github.com/muster-io/muster/internal/totp"
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
	SubmitSecondFactor(ctx context.Context, sess auth.Session, p auth.Proof, addr netip.Addr) (auth.Session, error)
	ChangePassword(ctx context.Context, sess auth.Session, c auth.PasswordChange) error
	Roles() auth.Roles
}

// Users is what the API needs of internal/users.
type Users interface {
	Get(ctx context.Context, id int64) (users.User, error)
	UpdateProfile(ctx context.Context, id int64, actor audit.Actor, p users.Profile, addr netip.Addr) (users.User, error)
}

// UserAdmin is what the API needs of the administration of users in internal/users.
type UserAdmin interface {
	List(ctx context.Context, f users.ListFilter) (users.Page, error)
	Get(ctx context.Context, id string) (users.User, error)
	Create(ctx context.Context, r users.Requester, n users.NewUser) (users.User, users.SetupLink, error)
	Update(ctx context.Context, r users.Requester, id string, version *int64, c users.Changes) (users.User, error)
	Disable(ctx context.Context, r users.Requester, id string) (users.User, error)
	Enable(ctx context.Context, r users.Requester, id string) (users.User, error)
	Delete(ctx context.Context, r users.Requester, id string, version *int64) error
	CreateSetupLink(ctx context.Context, r users.Requester, id string) (users.SetupLink, error)
	ConvertToLocal(ctx context.Context, r users.Requester, id string) (users.User, users.SetupLink, error)
	CompleteSetup(ctx context.Context, token, password string, addr netip.Addr) error
}

// UserDirectory is what the API needs of the user directory in internal/users.
type UserDirectory interface {
	List(ctx context.Context, q string, after *users.Cursor, limit int) (users.DirectoryPage, error)
}

// AuditLog is what the API needs of the Audit log reader in internal/audit.
type AuditLog interface {
	List(ctx context.Context, f audit.Filter) (audit.Page, error)
}

// TOTP is what the API needs of internal/totp.
type TOTP interface {
	Status(ctx context.Context, userID int64) (totp.Status, error)
	Begin(ctx context.Context, sess auth.Session) (totp.Enrolment, error)
	Confirm(ctx context.Context, sess auth.Session, code string, addr netip.Addr) ([]string, error)
	RegenerateRecoveryCodes(ctx context.Context, sess auth.Session, code string, addr netip.Addr) ([]string, error)
	Remove(ctx context.Context, sess auth.Session, p totp.Removal, addr netip.Addr) error
	Reset(ctx context.Context, actor audit.Actor, t audit.Transport, addr netip.Addr, publicID string) error
}

// Organization is what the API needs of the organization resource in internal/organization.
type Organization interface {
	Get(ctx context.Context) (organization.Organization, error)
	Update(ctx context.Context, actor audit.Actor, t audit.Transport, addr netip.Addr, version *int64,
		in organization.Input) (organization.Organization, error)
}

// Notices is what the API needs of the Organization-wide notices in internal/live.
type Notices interface {
	Visible(ctx context.Context, admin bool) ([]organization.Notice, error)
}

// Live is what the API needs of the live-updates Hub in internal/live.
type Live interface {
	Subscribe(s live.Subscriber) (*live.Subscription, error)
	Unsubscribe(sub *live.Subscription)
	Stream(ctx context.Context, w io.Writer, flush func() error, sub *live.Subscription) error
}

// Tokens is what the API needs of internal/tokens: bearer authentication, Personal access tokens and Service
// accounts.
type Tokens interface {
	Authenticate(ctx context.Context, value string, addr netip.Addr) (*auth.Identity, error)
	ListPersonal(ctx context.Context, ownerID int64) ([]tokens.Token, error)
	CreatePersonal(ctx context.Context, r tokens.Requester, owner tokens.Owner, held []auth.Permission,
		n tokens.NewPersonal) (tokens.Created, error)
	RevokePersonal(ctx context.Context, r tokens.Requester, owner tokens.Owner, publicID string) error
	ListServiceAccounts(ctx context.Context, f tokens.ListFilter) (tokens.Page, error)
	GetServiceAccount(ctx context.Context, publicID string) (tokens.ServiceAccount, error)
	CreateServiceAccount(ctx context.Context, r tokens.Requester, in tokens.ServiceAccountInput) (tokens.ServiceAccount,
		error)
	UpdateServiceAccount(ctx context.Context, r tokens.Requester, publicID string, version *int64,
		in tokens.ServiceAccountInput) (tokens.ServiceAccount, error)
	DisableServiceAccount(ctx context.Context, r tokens.Requester, publicID string) (tokens.ServiceAccount, error)
	EnableServiceAccount(ctx context.Context, r tokens.Requester, publicID string) (tokens.ServiceAccount, error)
	DeleteServiceAccount(ctx context.Context, r tokens.Requester, publicID string, version *int64) error
	ListServiceAccountTokens(ctx context.Context, publicID string) ([]tokens.Token, error)
	CreateServiceAccountToken(ctx context.Context, r tokens.Requester, publicID string, n tokens.NewToken) (
		tokens.Created, error)
	RevokeServiceAccountToken(ctx context.Context, r tokens.Requester, publicID, tokenID string) error
}

// Integrations is what the API needs of internal/integrations: Integrations and their tokens.
type Integrations interface {
	List(ctx context.Context, f integrations.ListFilter) (integrations.Page, error)
	Get(ctx context.Context, publicID string) (integrations.Integration, error)
	Create(ctx context.Context, r integrations.Requester, in integrations.Input) (integrations.Integration, error)
	Update(ctx context.Context, r integrations.Requester, publicID string, version *int64, in integrations.Input) (
		integrations.Integration, error)
	Delete(ctx context.Context, r integrations.Requester, publicID string, version *int64) error
	ListTokens(ctx context.Context, publicID string) ([]integrations.Token, error)
	CreateToken(ctx context.Context, r integrations.Requester, publicID, name string) (integrations.CreatedToken, error)
	RevokeToken(ctx context.Context, r integrations.Requester, publicID, tokenID string) error
	IngestURL() string
	HeartbeatURL() string
}

// StoredSnapshots is what the API needs of the Stored Snapshot reads of internal/ingest.
type StoredSnapshots interface {
	List(ctx context.Context, f ingest.ListFilter) (ingest.Page, error)
	Get(ctx context.Context, publicID string) (ingest.Snapshot, error)
}

// Alerts is what the API needs of the Alerts view of internal/ingest and of the learned Alertmanager routes.
type Alerts interface {
	List(ctx context.Context, f ingest.AlertFilter) (ingest.AlertPage, error)
	Routes(ctx context.Context, integration string) ([]ingest.AlertmanagerRoute, error)
}

// Routes is what the API needs of internal/routing: Routes and their order, the Group key preview and the Route
// suggestions.
type Routes interface {
	List(ctx context.Context) (routing.List, error)
	Get(ctx context.Context, publicID string) (routing.Route, error)
	Create(ctx context.Context, r routing.Requester, in routing.Input) (routing.Route, error)
	Update(ctx context.Context, r routing.Requester, publicID string, version *int64, in routing.Input) (
		routing.Route, error)
	Delete(ctx context.Context, r routing.Requester, publicID string, version *int64) error
	Reorder(ctx context.Context, r routing.Requester, version *int64, ids []string) (routing.List, error)
	Preview(ctx context.Context, req routing.PreviewRequest) (routing.Preview, error)
	Suggestions(ctx context.Context, userID *int64) ([]routing.Suggestion, error)
	AcceptSuggestion(ctx context.Context, r routing.Requester, id string, destinationIDs []string) (routing.Route,
		error)
	DismissSuggestion(ctx context.Context, userID int64, id string) error
}

// Config is what the API serves with.
type Config struct {
	Sessions     Sessions
	Users        Users
	Admin        UserAdmin
	AuditLog     AuditLog
	TOTP         TOTP
	Organization Organization
	Notices      Notices
	Live         Live
	OIDC         OIDC
	Tokens       Tokens
	Integrations Integrations
	Snapshots    StoredSnapshots
	Alerts       Alerts
	Routes       Routes
	AlertGroups  AlertGroups
	Commands     Commands
	Directory    UserDirectory
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
	admin          UserAdmin
	auditLog       AuditLog
	totp           TOTP
	organization   Organization
	notices        Notices
	live           Live
	oidc           OIDC
	tokens         Tokens
	integrations   Integrations
	snapshots      StoredSnapshots
	alerts         Alerts
	routes         Routes
	alertGroups    AlertGroups
	commands       Commands
	directory      UserDirectory
	trustedProxies []netip.Prefix
	log            *logging.Logger
	real           clock.Clock

	router     routers.Router
	operations map[*openapi3.Operation]*operation
	// ifMatchRequired are the operations whose If-Match header is required.
	ifMatchRequired map[string]bool
	validated       http.Handler
}

// implemented are the operations this build serves, by the method names of the strict server.
var implemented = map[string]bool{
	"GetSignInOptions": true, "CreateSession": true, "GetCurrentSession": true, "DeleteCurrentSession": true,
	"GetMe": true, "UpdateMe": true, "ChangePassword": true, "ListMySessions": true, "DeleteMySessions": true,
	"ListRoles": true, "GetOpenApiSpec": true,
	"ListUsers": true, "CreateUser": true, "GetUser": true, "UpdateUser": true, "DeleteUser": true, "DisableUser": true,
	"EnableUser": true, "CreatePasswordSetupLink": true, "CompletePasswordSetup": true, "ListAuditLog": true,
	"GetMyTotp": true, "BeginTotpEnrolment": true, "ConfirmTotpEnrolment": true, "RemoveTotp": true,
	"RegenerateTotpRecoveryCodes": true, "SubmitSessionTotp": true, "ResetUserTotp": true, "GetOrganization": true,
	"UpdateOrganization": true, "ListSystemNotices": true, "StreamLiveUpdates": true,
	"GetOidcSettings": true, "UpdateOidcSettings": true, "CheckOidcSettings": true, "StartOidcSignIn": true,
	"CompleteOidcSignIn": true, "StartOidcLink": true, "CompleteOidcLink": true, "ConvertUserToLocal": true,
	"ListPersonalAccessTokens": true, "CreatePersonalAccessToken": true, "RevokePersonalAccessToken": true,
	"ListServiceAccounts": true, "CreateServiceAccount": true, "GetServiceAccount": true, "UpdateServiceAccount": true,
	"DeleteServiceAccount": true, "DisableServiceAccount": true, "EnableServiceAccount": true,
	"ListServiceAccountTokens": true, "CreateServiceAccountToken": true, "RevokeServiceAccountToken": true,
	"ListIntegrations": true, "CreateIntegration": true, "GetIntegration": true, "UpdateIntegration": true,
	"DeleteIntegration": true, "ListIntegrationTokens": true, "CreateIntegrationToken": true,
	"RevokeIntegrationToken": true, "ListStoredSnapshots": true, "GetStoredSnapshot": true,
	"ListIntegrationAlerts": true, "ListAlertmanagerRoutes": true,
	"ListRoutes": true, "CreateRoute": true, "GetRoute": true, "UpdateRoute": true, "DeleteRoute": true,
	"ReorderRoutes": true, "ListRouteProfiles": true, "PreviewGroupKey": true, "ListRouteSuggestions": true,
	"AcceptRouteSuggestion": true, "DismissRouteSuggestion": true,
	"GetAlertGroup": true, "ListAlertGroupAlerts": true, "GetAlertGroupTimeline": true, "MoveOpenAlertGroups": true,
	"ListAlertGroups": true, "GetAlertGroupCounts": true, "ListRelatedAlertGroups": true,
	"GetAlertGroupStatistics": true,
	"AcknowledgeAlertGroup":   true, "UnacknowledgeAlertGroup": true, "ResolveAlertGroup": true,
	"UnresolveAlertGroup": true, "SnoozeAlertGroup": true, "UnsnoozeAlertGroup": true, "RunBulkCommand": true,
	"ListAlertGroupNotes": true, "CreateAlertGroupNote": true, "ListUserDirectory": true,
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
		sessions: cfg.Sessions, users: cfg.Users, admin: cfg.Admin, auditLog: cfg.AuditLog, totp: cfg.TOTP,
		organization: cfg.Organization, notices: cfg.Notices, live: cfg.Live, oidc: cfg.OIDC, tokens: cfg.Tokens,
		integrations: cfg.Integrations, snapshots: cfg.Snapshots, alerts: cfg.Alerts, routes: cfg.Routes,
		alertGroups: cfg.AlertGroups, commands: cfg.Commands, directory: cfg.Directory,
		trustedProxies: cfg.TrustedProxies, log: cfg.Log, real: cfg.Real,
		router: router, operations: readOperations(doc), ifMatchRequired: ifMatchRequired(doc),
	}
	mux := http.NewServeMux()
	strict := gen.NewStrictHandlerWithOptions(s, []gen.StrictMiddlewareFunc{notImplemented, withOIDCState},
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
	s.validated = s.preconditions(s.validator(doc)(s.permit(mux)))
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
	tag := etag(u.Version)
	method := gen.UserSignInMethod(u.SignInMethod())
	offline, locked := u.OfflineAccess, u.RoleLocked
	out := gen.User{
		Id: u.PublicID, Name: u.Name, Login: u.Login, Role: gen.RoleName(u.Role), Source: gen.UserSource(u.Source),
		Status: gen.UserStatus(u.Status), TotpEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt.UTC(), Etag: &tag,
		SignInMethod: &method, RoleLocked: &locked,
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
