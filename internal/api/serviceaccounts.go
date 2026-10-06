// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/tokens"
)

// serviceAccountsCursor names the cursors of listServiceAccounts.
const serviceAccountsCursor = "service-accounts"

// serviceAccountKey is the sort key of a listServiceAccounts cursor.
type serviceAccountKey struct {
	ID int64 `json:"i"`
}

// ListServiceAccounts is listServiceAccounts: the Service accounts that are not deleted, in the order they were
// created.
func (s *Server) ListServiceAccounts(ctx context.Context, req gen.ListServiceAccountsRequestObject) (
	gen.ListServiceAccountsResponseObject, error) {
	f := tokens.ListFilter{Limit: pageSize(req.Params.Limit)}
	var key serviceAccountKey
	if ok, err := decodeCursor(req.Params.Cursor, serviceAccountsCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &key.ID
	}
	page, err := s.tokens.ListServiceAccounts(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ServiceAccountList{Items: make([]gen.ServiceAccount, 0, len(page.ServiceAccounts))}
	for _, sa := range page.ServiceAccounts {
		out.Items = append(out.Items, serviceAccountOf(sa))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(serviceAccountsCursor, serviceAccountKey{ID: *page.Next}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListServiceAccounts200JSONResponse(out), nil
}

// CreateServiceAccount is createServiceAccount: an active Service account with a Role and no tokens yet.
func (s *Server) CreateServiceAccount(ctx context.Context, req gen.CreateServiceAccountRequestObject) (
	gen.CreateServiceAccountResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	if err := s.roleAssignable(ctx, string(req.Body.Role)); err != nil {
		return nil, err
	}
	sa, err := s.tokens.CreateServiceAccount(ctx, r, serviceAccountInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(sa.Version), BasePath+"/service-accounts/"+sa.PublicID
	return gen.CreateServiceAccount201JSONResponse{
		Body:    serviceAccountOf(sa),
		Headers: gen.CreateServiceAccount201ResponseHeaders{ETag: &tag, Location: &location},
	}, nil
}

// GetServiceAccount is getServiceAccount.
func (s *Server) GetServiceAccount(ctx context.Context, req gen.GetServiceAccountRequestObject) (
	gen.GetServiceAccountResponseObject, error) {
	sa, err := s.tokens.GetServiceAccount(ctx, req.ServiceAccountId)
	if err != nil {
		return nil, err
	}
	tag := etag(sa.Version)
	return gen.GetServiceAccount200JSONResponse{Body: serviceAccountOf(sa),
		Headers: gen.GetServiceAccount200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateServiceAccount is updateServiceAccount: the name and the Role, with If-Match.
func (s *Server) UpdateServiceAccount(ctx context.Context, req gen.UpdateServiceAccountRequestObject) (
	gen.UpdateServiceAccountResponseObject, error) {
	r, _, err := tokenRequester(ctx)
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
	if err := s.roleAssignable(ctx, string(req.Body.Role)); err != nil {
		return nil, err
	}
	sa, err := s.tokens.UpdateServiceAccount(ctx, r, req.ServiceAccountId, version, serviceAccountInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(sa.Version)
	return gen.UpdateServiceAccount200JSONResponse{Body: serviceAccountOf(sa),
		Headers: gen.UpdateServiceAccount200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteServiceAccount is deleteServiceAccount: the account goes and its tokens are revoked.
func (s *Server) DeleteServiceAccount(ctx context.Context, req gen.DeleteServiceAccountRequestObject) (
	gen.DeleteServiceAccountResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	if err := s.tokens.DeleteServiceAccount(ctx, r, req.ServiceAccountId, version); err != nil {
		return nil, err
	}
	return gen.DeleteServiceAccount204Response{}, nil
}

// DisableServiceAccount is disableServiceAccount: its tokens stop working until it is enabled.
func (s *Server) DisableServiceAccount(ctx context.Context, req gen.DisableServiceAccountRequestObject) (
	gen.DisableServiceAccountResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	sa, err := s.tokens.DisableServiceAccount(ctx, r, req.ServiceAccountId)
	if err != nil {
		return nil, err
	}
	return gen.DisableServiceAccount200JSONResponse(serviceAccountOf(sa)), nil
}

// EnableServiceAccount is enableServiceAccount: its tokens work again.
func (s *Server) EnableServiceAccount(ctx context.Context, req gen.EnableServiceAccountRequestObject) (
	gen.EnableServiceAccountResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	sa, err := s.tokens.EnableServiceAccount(ctx, r, req.ServiceAccountId)
	if err != nil {
		return nil, err
	}
	return gen.EnableServiceAccount200JSONResponse(serviceAccountOf(sa)), nil
}

// ListServiceAccountTokens is listServiceAccountTokens: the tokens that are not revoked, without values.
func (s *Server) ListServiceAccountTokens(ctx context.Context, req gen.ListServiceAccountTokensRequestObject) (
	gen.ListServiceAccountTokensResponseObject, error) {
	list, err := s.tokens.ListServiceAccountTokens(ctx, req.ServiceAccountId)
	if err != nil {
		return nil, err
	}
	out := gen.ServiceAccountTokenList{Items: make([]gen.ServiceAccountToken, 0, len(list))}
	for _, t := range list {
		out.Items = append(out.Items, serviceAccountTokenOf(t))
	}
	return gen.ListServiceAccountTokens200JSONResponse(out), nil
}

// CreateServiceAccountToken is createServiceAccountToken: a token whose value is shown this once. The middleware
// lets only the web session through (C-04.FR-7).
func (s *Server) CreateServiceAccountToken(ctx context.Context, req gen.CreateServiceAccountTokenRequestObject) (
	gen.CreateServiceAccountTokenResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	created, err := s.tokens.CreateServiceAccountToken(ctx, r, req.ServiceAccountId, tokens.NewToken{
		Name: req.Body.Name, ExpiresAt: optionalTime(req.Body.ExpiresAt),
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateServiceAccountToken201JSONResponse(gen.ServiceAccountTokenCreated{
		Token: serviceAccountTokenOf(created.Token), Value: created.Value,
	}), nil
}

// RevokeServiceAccountToken is revokeServiceAccountToken: the token stops working at once.
func (s *Server) RevokeServiceAccountToken(ctx context.Context, req gen.RevokeServiceAccountTokenRequestObject) (
	gen.RevokeServiceAccountTokenResponseObject, error) {
	r, _, err := tokenRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tokens.RevokeServiceAccountToken(ctx, r, req.ServiceAccountId, req.TokenId); err != nil {
		return nil, err
	}
	return gen.RevokeServiceAccountToken204Response{}, nil
}

func serviceAccountInputOf(in gen.ServiceAccountInput) tokens.ServiceAccountInput {
	return tokens.ServiceAccountInput{Name: in.Name, Role: string(in.Role)}
}

func serviceAccountOf(sa tokens.ServiceAccount) gen.ServiceAccount {
	tag := etag(sa.Version)
	return gen.ServiceAccount{Id: sa.PublicID, Name: sa.Name, Role: gen.RoleName(sa.Role),
		Status: gen.ServiceAccountStatus(sa.Status), TokenCount: int(sa.TokenCount), CreatedAt: sa.CreatedAt.UTC(),
		Etag: &tag}
}
