// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/users"
)

// usersCursor names the cursors of listUsers.
const usersCursor = "users"

// userKey is the sort key of a listUsers cursor.
type userKey struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// requester is who asks for a change through the API: the identity of the request, its Transport and address.
func requester(ctx context.Context) (users.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return users.Requester{}, err
	}
	return users.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListUsers is listUsers: users in the order of their name, with the filters q, role, status and source.
func (s *Server) ListUsers(ctx context.Context, req gen.ListUsersRequestObject) (gen.ListUsersResponseObject, error) {
	f := users.ListFilter{Limit: pageSize(req.Params.Limit)}
	if req.Params.Q != nil {
		f.Q = *req.Params.Q
	}
	if req.Params.Role != nil {
		f.Role = string(*req.Params.Role)
	}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	if req.Params.Source != nil {
		f.Source = string(*req.Params.Source)
	}
	var key userKey
	if ok, err := decodeCursor(req.Params.Cursor, usersCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &users.Cursor{Name: key.Name, ID: key.ID}
	}
	page, err := s.admin.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.UserList{Items: make([]gen.User, 0, len(page.Users))}
	for _, u := range page.Users {
		out.Items = append(out.Items, userOf(u))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(usersCursor, userKey{Name: page.Next.Name, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListUsers200JSONResponse(out), nil
}

// CreateUser is createUser: a local user and the single-use link that sets its first password (C-03.FR-3).
func (s *Server) CreateUser(ctx context.Context, req gen.CreateUserRequestObject) (gen.CreateUserResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	if err := s.roleAssignable(ctx, string(req.Body.Role)); err != nil {
		return nil, err
	}
	n := users.NewUser{Name: req.Body.Name, Login: req.Body.Login, Role: string(req.Body.Role)}
	if req.Body.Email.IsSpecified() && !req.Body.Email.IsNull() {
		email := req.Body.Email.MustGet()
		n.Email = &email
	}
	u, link, err := s.admin.Create(ctx, r, n)
	if err != nil {
		return nil, err
	}
	tag, location := etag(u.Version), BasePath+"/users/"+u.PublicID
	return gen.CreateUser201JSONResponse{
		Body:    gen.UserCreated{User: userOf(u), PasswordSetupLink: linkOf(link)},
		Headers: gen.CreateUser201ResponseHeaders{ETag: &tag, Location: &location},
	}, nil
}

// GetUser is getUser, deleted users included.
func (s *Server) GetUser(ctx context.Context, req gen.GetUserRequestObject) (gen.GetUserResponseObject, error) {
	u, err := s.admin.Get(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	tag := etag(u.Version)
	return gen.GetUser200JSONResponse{Body: userOf(u), Headers: gen.GetUser200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateUser is updateUser: name, email and Role, with If-Match. An omitted email keeps its value and null clears it;
// a Role change ends the user's sessions.
func (s *Server) UpdateUser(ctx context.Context, req gen.UpdateUserRequestObject) (gen.UpdateUserResponseObject, error) {
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
	if err := s.roleAssignable(ctx, string(req.Body.Role)); err != nil {
		return nil, err
	}
	c := users.Changes{Name: req.Body.Name, Role: string(req.Body.Role), EmailSet: req.Body.Email.IsSpecified()}
	if c.EmailSet && !req.Body.Email.IsNull() {
		email := req.Body.Email.MustGet()
		c.Email = &email
	}
	u, err := s.admin.Update(ctx, r, req.UserId, version, c)
	if err != nil {
		return nil, err
	}
	tag := etag(u.Version)
	return gen.UpdateUser200JSONResponse{Body: userOf(u), Headers: gen.UpdateUser200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteUser is deleteUser: pseudonymization (C-03.FR-13), with an optional If-Match.
func (s *Server) DeleteUser(ctx context.Context, req gen.DeleteUserRequestObject) (gen.DeleteUserResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	var version *int64
	if req.Params.IfMatch != nil {
		if version, err = ifMatch(*req.Params.IfMatch); err != nil {
			return nil, err
		}
	}
	if err := s.admin.Delete(ctx, r, req.UserId, version); err != nil {
		return nil, err
	}
	return gen.DeleteUser204Response{}, nil
}

// DisableUser is disableUser: the user's sessions end and sign-in is refused until enableUser.
func (s *Server) DisableUser(ctx context.Context, req gen.DisableUserRequestObject) (gen.DisableUserResponseObject,
	error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.admin.Disable(ctx, r, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.DisableUser200JSONResponse(userOf(u)), nil
}

// EnableUser is enableUser.
func (s *Server) EnableUser(ctx context.Context, req gen.EnableUserRequestObject) (gen.EnableUserResponseObject,
	error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.admin.Enable(ctx, r, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.EnableUser200JSONResponse(userOf(u)), nil
}

// CreatePasswordSetupLink is createPasswordSetupLink: a new single-use link that supersedes the user's older ones.
func (s *Server) CreatePasswordSetupLink(ctx context.Context, req gen.CreatePasswordSetupLinkRequestObject) (
	gen.CreatePasswordSetupLinkResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	link, err := s.admin.CreateSetupLink(ctx, r, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.CreatePasswordSetupLink201JSONResponse(linkOf(link)), nil
}

func linkOf(l users.SetupLink) gen.PasswordSetupLink {
	return gen.PasswordSetupLink{Url: l.URL, ExpiresAt: l.ExpiresAt.UTC()}
}

// ResetUserTotp is resetUserTotp: an Admin removes a user's TOTP and recovery codes, which ends the user's sessions
// and is recorded in the Audit log (C-03.FR-11).
func (s *Server) ResetUserTotp(ctx context.Context, req gen.ResetUserTotpRequestObject) (
	gen.ResetUserTotpResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.totp.Reset(ctx, r.Actor, r.Transport, r.Address, req.UserId); err != nil {
		return nil, err
	}
	return gen.ResetUserTotp204Response{}, nil
}

// ConvertUserToLocal is convertUserToLocal: an Admin converts an account that signs in through OIDC back to local; the
// identity goes, the sessions end and the answer carries a password setup link (C-03.FR-29).
func (s *Server) ConvertUserToLocal(ctx context.Context, req gen.ConvertUserToLocalRequestObject) (
	gen.ConvertUserToLocalResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	u, link, err := s.admin.ConvertToLocal(ctx, r, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.ConvertUserToLocal200JSONResponse(gen.UserCreated{User: userOf(u), PasswordSetupLink: linkOf(link)}), nil
}
