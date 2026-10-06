// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/tokens"
)

// tokenRequester is who asks for a change of tokens or Service accounts through the API.
func tokenRequester(ctx context.Context) (tokens.Requester, *auth.Identity, error) {
	id, err := identity(ctx)
	if err != nil {
		return tokens.Requester{}, nil, err
	}
	return tokens.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, id, nil
}

// ownerOf is the user whose Personal access tokens the identity manages: the user of the session, or the owner of
// the Personal access token the request uses.
func ownerOf(id *auth.Identity) tokens.Owner {
	return tokens.Owner{ID: id.Session.User.ID, PublicID: id.Session.User.PublicID}
}

// ListPersonalAccessTokens is listPersonalAccessTokens: the caller's tokens that are not revoked, without values.
func (s *Server) ListPersonalAccessTokens(ctx context.Context, _ gen.ListPersonalAccessTokensRequestObject) (
	gen.ListPersonalAccessTokensResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.tokens.ListPersonal(ctx, id.Session.User.ID)
	if err != nil {
		return nil, err
	}
	out := gen.PersonalAccessTokenList{Items: make([]gen.PersonalAccessToken, 0, len(list))}
	for _, t := range list {
		out.Items = append(out.Items, personalTokenOf(t))
	}
	return gen.ListPersonalAccessTokens200JSONResponse(out), nil
}

// CreatePersonalAccessToken is createPersonalAccessToken: a token narrowed to Permissions the caller holds now, its
// value shown this once (C-04.FR-3, FR-7). The middleware lets only the web session through.
func (s *Server) CreatePersonalAccessToken(ctx context.Context, req gen.CreatePersonalAccessTokenRequestObject) (
	gen.CreatePersonalAccessTokenResponseObject, error) {
	r, id, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	n := tokens.NewPersonal{Name: req.Body.Name, ExpiresAt: optionalTime(req.Body.ExpiresAt)}
	for _, p := range req.Body.Permissions {
		n.Permissions = append(n.Permissions, auth.Permission(p))
	}
	created, err := s.tokens.CreatePersonal(ctx, r, ownerOf(id), id.Permissions, n)
	if err != nil {
		return nil, err
	}
	return gen.CreatePersonalAccessToken201JSONResponse(gen.PersonalAccessTokenCreated{
		Token: personalTokenOf(created.Token), Value: created.Value,
	}), nil
}

// RevokePersonalAccessToken is revokePersonalAccessToken: the caller's token stops working at once.
func (s *Server) RevokePersonalAccessToken(ctx context.Context, req gen.RevokePersonalAccessTokenRequestObject) (
	gen.RevokePersonalAccessTokenResponseObject, error) {
	r, id, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tokens.RevokePersonal(ctx, r, ownerOf(id), req.TokenId); err != nil {
		return nil, err
	}
	return gen.RevokePersonalAccessToken204Response{}, nil
}

func personalTokenOf(t tokens.Token) gen.PersonalAccessToken {
	out := gen.PersonalAccessToken{Id: t.PublicID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(),
		Permissions: permissionsOf(t.Permissions), ExpiresAt: nullableTime(t.ExpiresAt),
		LastUsedAt: nullableTime(t.LastUsedAt)}
	out.LastUsedAddress.SetNull()
	if t.LastUsedAddress.IsValid() {
		out.LastUsedAddress.Set(t.LastUsedAddress.String())
	}
	return out
}

func serviceAccountTokenOf(t tokens.Token) gen.ServiceAccountToken {
	out := gen.ServiceAccountToken{Id: t.PublicID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(),
		ExpiresAt: nullableTime(t.ExpiresAt), LastUsedAt: nullableTime(t.LastUsedAt)}
	out.LastUsedAddress.SetNull()
	if t.LastUsedAddress.IsValid() {
		out.LastUsedAddress.Set(t.LastUsedAddress.String())
	}
	return out
}

// nullableTime is t as a nullable field: null when it is nil.
func nullableTime(t *time.Time) nullable.Nullable[time.Time] {
	var out nullable.Nullable[time.Time]
	if t == nil {
		out.SetNull()
	} else {
		out.Set(t.UTC())
	}
	return out
}

// optionalTime is a nullable request field as a time, nil when it is omitted or null.
func optionalTime(v nullable.Nullable[time.Time]) *time.Time {
	if !v.IsSpecified() || v.IsNull() {
		return nil
	}
	t := v.MustGet()
	return &t
}
