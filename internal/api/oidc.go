// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/proxyconf"
)

// OIDC is what the API needs of internal/oidc.
type OIDC interface {
	Get(ctx context.Context) (oidc.Settings, error)
	Update(ctx context.Context, r oidc.Requester, version *int64, in oidc.Input) (oidc.Settings, error)
	Check(ctx context.Context) (oidc.CheckResult, error)
	SignInOptions(ctx context.Context) (bool, string, error)
	StartSignIn(ctx context.Context, returnTo string) (oidc.Start, error)
	CompleteSignIn(ctx context.Context, cb oidc.Callback) oidc.Outcome
}

// GetOidcSettings is getOidcSettings: the client secret and the proxy password only as their status (C-03.FR-21).
func (s *Server) GetOidcSettings(ctx context.Context, _ gen.GetOidcSettingsRequestObject) (
	gen.GetOidcSettingsResponseObject, error) {
	st, err := s.oidc.Get(ctx)
	if err != nil {
		return nil, err
	}
	body := oidcSettingsOf(st)
	return gen.GetOidcSettings200JSONResponse{Body: body,
		Headers: gen.GetOidcSettings200ResponseHeaders{ETag: body.Etag}}, nil
}

// UpdateOidcSettings is updateOidcSettings, with If-Match: omitted optional fields keep their value, and the Secrets
// follow the rule of every Secret field.
func (s *Server) UpdateOidcSettings(ctx context.Context, req gen.UpdateOidcSettingsRequestObject) (
	gen.UpdateOidcSettingsResponseObject, error) {
	r, err := requester(ctx)
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
	b := req.Body
	in := oidc.Input{
		Enabled: b.Enabled, DisplayName: b.DisplayName, IssuerURL: b.IssuerUrl, ClientID: b.ClientId,
		Scopes: b.Scopes, GroupsClaim: b.GroupsClaim, UnmatchedRole: string(b.UnmatchedRole), SyncRole: b.SyncRole,
		SkipTOTPWithIDPMFA: b.SkipTotpWithIdpMfa, ExpiresOnSet: b.ClientSecretExpiresOn.IsSpecified(),
		Proxy: proxyInput(b.Proxy),
	}
	if b.ClientSecret != nil {
		in.ClientSecret = keyring.Replace(logging.Secret(*b.ClientSecret))
	}
	if in.ExpiresOnSet && !b.ClientSecretExpiresOn.IsNull() {
		d := b.ClientSecretExpiresOn.MustGet().Time
		in.ClientSecretExpiresOn = &d
	}
	for _, m := range b.GroupMappings {
		in.GroupMappings = append(in.GroupMappings, oidc.GroupMapping{Group: m.Group, Role: string(m.Role)})
	}
	st, err := s.oidc.Update(ctx, oidc.Requester{Actor: r.Actor, Transport: r.Transport, Address: r.Address}, version,
		in)
	if err != nil {
		return nil, err
	}
	body := oidcSettingsOf(st)
	return gen.UpdateOidcSettings200JSONResponse{Body: body,
		Headers: gen.UpdateOidcSettings200ResponseHeaders{ETag: body.Etag}}, nil
}

// proxyInput is the proxy object of an update: omitted fields keep their value, a null username or password clears it.
func proxyInput(p gen.ProxyConfigInput) proxyconf.Input {
	in := proxyconf.Input{Enabled: p.Enabled, Address: p.Address, UsernameSet: p.Username.IsSpecified()}
	if p.Type != nil {
		t := string(*p.Type)
		in.Type = &t
	}
	if in.UsernameSet && !p.Username.IsNull() {
		u := p.Username.MustGet()
		in.Username = &u
	}
	switch {
	case !p.Password.IsSpecified():
	case p.Password.IsNull():
		in.Password = keyring.Clear
	default:
		in.Password = keyring.Replace(logging.Secret(p.Password.MustGet()))
	}
	return in
}

// CheckOidcSettings is checkOidcSettings: discovery with the saved settings (C-03.FR-8).
func (s *Server) CheckOidcSettings(ctx context.Context, _ gen.CheckOidcSettingsRequestObject) (
	gen.CheckOidcSettingsResponseObject, error) {
	res, err := s.oidc.Check(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.CheckOidcSettings200JSONResponse{Ok: res.OK, Via: gen.ConnectionPathDirect}
	if res.ViaProxy {
		out.Via = gen.ConnectionPathProxy
	}
	warnings := warningsOf(res.Warnings)
	out.Warnings = &warnings
	if res.OK {
		latency := int(res.Latency / time.Millisecond)
		out.LatencyMs = &latency
		d := res.Discovery
		out.Issuer.Set(d.Issuer)
		out.AuthorizationEndpoint.Set(d.AuthorizationEndpoint)
		out.TokenEndpoint.Set(d.TokenEndpoint)
		out.JwksUri.Set(d.JWKSURI)
		out.Error.SetNull()
	} else {
		out.Error.Set(res.Error)
	}
	return out, nil
}

func oidcSettingsOf(st oidc.Settings) gen.OidcSettings {
	tag := etag(st.Version)
	scopes := st.Scopes
	out := gen.OidcSettings{
		Enabled: st.Enabled, IssuerUrl: st.IssuerURL, ClientId: st.ClientID,
		ClientSecretStatus: keyStatusOf(st.ClientSecret), Scopes: &scopes, GroupsClaim: st.GroupsClaim,
		GroupMappings: make([]gen.OidcGroupMapping, 0, len(st.GroupMappings)),
		UnmatchedRole: gen.OidcUnmatchedRole(st.UnmatchedRole), SyncRole: st.SyncRole,
		SkipTotpWithIdpMfa: st.SkipTOTPWithIDPMFA, Proxy: proxyOf(st.Proxy, st.ProxyPassword),
		Warnings: warningsOf(st.Warnings), Etag: &tag,
	}
	// Only a display name the Admin set is returned, so that a read sent back unchanged does not turn the default —
	// the host of the issuer URL — into a fixed name.
	if st.DisplayName != "" {
		display := st.DisplayName
		out.DisplayName = &display
	}
	for _, m := range st.GroupMappings {
		out.GroupMappings = append(out.GroupMappings, gen.OidcGroupMapping{Group: m.Group, Role: gen.RoleName(m.Role)})
	}
	if st.ClientSecretExpiresOn != nil {
		out.ClientSecretExpiresOn.Set(openapi_types.Date{Time: *st.ClientSecretExpiresOn})
	} else {
		out.ClientSecretExpiresOn.SetNull()
	}
	if st.UpdatedAt != nil {
		out.UpdatedAt.Set(*st.UpdatedAt)
	} else {
		out.UpdatedAt.SetNull()
	}
	return out
}

func keyStatusOf(st keyring.SecretStatus) gen.SecretStatus {
	out := gen.SecretStatus{Set: st.Set}
	if st.UpdatedAt != nil {
		out.UpdatedAt.Set(st.UpdatedAt.UTC())
	} else {
		out.UpdatedAt.SetNull()
	}
	return out
}

func proxyOf(c proxyconf.Config, password keyring.SecretStatus) gen.ProxyConfig {
	status := keyStatusOf(password)
	out := gen.ProxyConfig{Enabled: c.Enabled, PasswordStatus: &status}
	if c.Type != "" {
		t := gen.ProxyType(c.Type)
		out.Type = &t
	}
	if c.Address != "" {
		a := c.Address
		out.Address = &a
	}
	if c.Username != nil {
		out.Username.Set(*c.Username)
	} else {
		out.Username.SetNull()
	}
	return out
}

func warningsOf(ws []oidc.Warning) []gen.OidcWarning {
	out := make([]gen.OidcWarning, 0, len(ws))
	for _, w := range ws {
		item := gen.OidcWarning{Kind: gen.OidcWarningKind(w.Kind)}
		if w.ExpiresOn != nil {
			item.ExpiresOn.Set(openapi_types.Date{Time: *w.ExpiresOn})
		}
		if w.User != nil {
			login := w.User.Login
			item.User = &gen.UserRef{Id: w.User.PublicID, Name: w.User.Name, Login: &login}
		}
		if w.Role != "" {
			role := gen.RoleName(w.Role)
			item.Role = &role
		}
		out = append(out, item)
	}
	return out
}
