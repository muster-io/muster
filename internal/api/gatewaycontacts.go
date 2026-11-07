// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/gateways"
	"github.com/muster-io/muster/internal/publicid"
)

const (
	// typeGatewayRefused is a notification that a person waits for and its Notification gateway refused.
	typeGatewayRefused = "gateway-refused"
	// codeContactCodeRequestsExceeded refuses a code beyond gateway.contact_code_requests.
	codeContactCodeRequestsExceeded = "contact_code_requests_exceeded"
	// codeGatewayRefused is the code of typeGatewayRefused.
	codeGatewayRefused = "gateway_refused"
	// detailOnlyThePerson refuses an Admin's attempt to set the contact of another User (C-30.AC-2).
	detailOnlyThePerson = "Only the person sets their contact, from their own web session."
)

// GatewayContacts is what the API needs of internal/gateways.
type GatewayContacts interface {
	List(ctx context.Context, userID int64) ([]gateways.Contact, error)
	ListOfUser(ctx context.Context, userPublicID string) ([]gateways.Contact, error)
	Create(ctx context.Context, r gateways.Requester, connectionPublicID, contact string) (gateways.Contact, error)
	SendCode(ctx context.Context, r gateways.Requester, contactPublicID string) (gateways.Contact, error)
	Confirm(ctx context.Context, r gateways.Requester, contactPublicID, code string) (gateways.Contact, error)
	Delete(ctx context.Context, r gateways.Requester, contactPublicID string) error
	DeleteOfUser(ctx context.Context, r gateways.Requester, userPublicID, contactPublicID string) error
}

// contactRequester is who asks: the signed-in User — the owner of a Personal access token — with the web session a
// code is tied to, none for a token, and the actor, the Transport and the client address of the Audit log entry.
func contactRequester(ctx context.Context) (gateways.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return gateways.Requester{}, err
	}
	return gateways.Requester{UserID: id.Session.User.ID, UserPublicID: id.Session.User.PublicID,
		SessionID: id.Session.ID, Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// contactProblem maps the errors of internal/gateways to their problems; any other error is returned as it is, a
// *delivery.LimitedError included, which problemFor answers with 503.
func contactProblem(err error) error {
	if f, ok := errors.AsType[*gateways.FieldError](err); ok {
		p := fieldProblem(http.StatusUnprocessableEntity, f.Pointer, f.Code, f.Detail)
		p.AttemptsRemaining = f.AttemptsRemaining
		return p
	}
	if l, ok := errors.AsType[*gateways.RateLimitedError](err); ok {
		p := problem(http.StatusTooManyRequests, typeRateLimited, codeContactCodeRequestsExceeded,
			"Too many codes were requested within an hour; wait before the next one.")
		p.RetryAfter = l.Seconds()
		return p
	}
	if r, ok := errors.AsType[*gateways.RefusedError](err); ok {
		p := problem(http.StatusBadGateway, typeGatewayRefused, codeGatewayRefused,
			"The Notification gateway refused the code; check the gateway and try again.")
		class := gen.ProblemGatewayClass(r.Class)
		p.GatewayClass = &class
		return p
	}
	switch {
	case errors.Is(err, gateways.ErrNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such contact.")
	case errors.Is(err, gateways.ErrUserNotFound):
		return problem(http.StatusNotFound, typeNotFound, "", "No such user.")
	}
	return err
}

// ListMyGatewayContacts is listMyGatewayContacts: the caller's contacts at the Notification gateways (C-30.FR-3).
func (s *Server) ListMyGatewayContacts(ctx context.Context, _ gen.ListMyGatewayContactsRequestObject) (
	gen.ListMyGatewayContactsResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.gatewayContacts.List(ctx, r.UserID)
	if err != nil {
		return nil, err
	}
	return gen.ListMyGatewayContacts200JSONResponse(gatewayContactListOf(list)), nil
}

// CreateMyGatewayContact is createMyGatewayContact: an unconfirmed contact of the caller, whose code the gateway
// delivers (C-30.FR-3); the middleware lets only the web session through.
func (s *Server) CreateMyGatewayContact(ctx context.Context, req gen.CreateMyGatewayContactRequestObject) (
	gen.CreateMyGatewayContactResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	c, err := s.gatewayContacts.Create(ctx, r, req.Body.ConnectionId, req.Body.Contact)
	if err != nil {
		return nil, contactProblem(err)
	}
	return gen.CreateMyGatewayContact202JSONResponse(gatewayContactOf(c)), nil
}

// SendMyGatewayContactCode is sendMyGatewayContactCode: a new code for a contact of the caller (C-30.FR-3).
func (s *Server) SendMyGatewayContactCode(ctx context.Context, req gen.SendMyGatewayContactCodeRequestObject) (
	gen.SendMyGatewayContactCodeResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.gatewayContacts.SendCode(ctx, r, req.GatewayContactId)
	if err != nil {
		return nil, contactProblem(err)
	}
	return gen.SendMyGatewayContactCode202JSONResponse(gatewayContactOf(c)), nil
}

// ConfirmMyGatewayContact is confirmMyGatewayContact: the code the gateway delivered confirms the caller's contact,
// in the web session that asked for it (C-30.FR-3, AC-2).
func (s *Server) ConfirmMyGatewayContact(ctx context.Context, req gen.ConfirmMyGatewayContactRequestObject) (
	gen.ConfirmMyGatewayContactResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	code := ""
	if req.Body.Code != nil {
		code = *req.Body.Code
	}
	c, err := s.gatewayContacts.Confirm(ctx, r, req.GatewayContactId, code)
	if err != nil {
		return nil, contactProblem(err)
	}
	return gen.ConfirmMyGatewayContact200JSONResponse(gatewayContactOf(c)), nil
}

// DeleteMyGatewayContact is deleteMyGatewayContact: the caller removes one of their contacts.
func (s *Server) DeleteMyGatewayContact(ctx context.Context, req gen.DeleteMyGatewayContactRequestObject) (
	gen.DeleteMyGatewayContactResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.gatewayContacts.Delete(ctx, r, req.GatewayContactId); err != nil {
		return nil, contactProblem(err)
	}
	return gen.DeleteMyGatewayContact204Response{}, nil
}

// ListUserGatewayContacts is listUserGatewayContacts: the contacts of a User, for users:read (C-18.FR-6 revised).
func (s *Server) ListUserGatewayContacts(ctx context.Context, req gen.ListUserGatewayContactsRequestObject) (
	gen.ListUserGatewayContactsResponseObject, error) {
	list, err := s.gatewayContacts.ListOfUser(ctx, req.UserId)
	if err != nil {
		return nil, contactProblem(err)
	}
	return gen.ListUserGatewayContacts200JSONResponse(gatewayContactListOf(list)), nil
}

// CreateUserGatewayContact is createUserGatewayContact, declared so that the API says it: an Admin never sets the
// contact of another User (C-30.AC-2, ADR-0013). It always answers 403 and changes nothing.
func (s *Server) CreateUserGatewayContact(context.Context, gen.CreateUserGatewayContactRequestObject) (
	gen.CreateUserGatewayContactResponseObject, error) {
	return nil, problem(http.StatusForbidden, typeForbidden, "", detailOnlyThePerson)
}

// DeleteUserGatewayContact is deleteUserGatewayContact: an Admin removes a contact of a User. An Admin's own contact
// is removed from the web session only, as under /me: with a token it is 403 session_required.
func (s *Server) DeleteUserGatewayContact(ctx context.Context, req gen.DeleteUserGatewayContactRequestObject) (
	gen.DeleteUserGatewayContactResponseObject, error) {
	r, err := contactRequester(ctx)
	if err != nil {
		return nil, err
	}
	if id, _ := identity(ctx); id.Token != nil && r.UserPublicID != "" {
		if norm, err := publicid.Parse(publicid.User, req.UserId); err == nil && norm == r.UserPublicID {
			return nil, errSessionRequired
		}
	}
	if err := s.gatewayContacts.DeleteOfUser(ctx, r, req.UserId, req.GatewayContactId); err != nil {
		return nil, contactProblem(err)
	}
	return gen.DeleteUserGatewayContact204Response{}, nil
}

func gatewayContactListOf(list []gateways.Contact) gen.GatewayContactList {
	out := gen.GatewayContactList{Items: make([]gen.GatewayContact, len(list))}
	for i, c := range list {
		out.Items[i] = gatewayContactOf(c)
	}
	return out
}

func gatewayContactOf(c gateways.Contact) gen.GatewayContact {
	out := gen.GatewayContact{Id: c.PublicID, Connection: gen.EntityRef{Id: c.ConnectionPublicID,
		Name: c.ConnectionName}, Contact: c.Contact, Confirmed: c.Confirmed(), ConfirmedAt: nullableTime(c.ConfirmedAt),
		CreatedAt: c.CreatedAt.UTC()}
	if c.Replaces != nil {
		out.Replaces.Set(*c.Replaces)
	} else {
		out.Replaces.SetNull()
	}
	return out
}
