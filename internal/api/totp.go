// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/totp"
)

// GetMyTotp is getMyTotp: whether the caller has TOTP, waits for a first code, and how many recovery codes remain.
func (s *Server) GetMyTotp(ctx context.Context, _ gen.GetMyTotpRequestObject) (gen.GetMyTotpResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.totp.Status(ctx, id.Session.User.ID)
	if err != nil {
		return nil, err
	}
	out := gen.TotpStatus{
		Enrolled: st.Enrolled, EnrolmentPending: st.Pending, RecoveryCodesRemaining: int(st.RecoveryCodesRemaining),
	}
	if st.EnrolledAt != nil {
		out.EnrolledAt.Set(st.EnrolledAt.UTC())
	} else {
		out.EnrolledAt.SetNull()
	}
	return gen.GetMyTotp200JSONResponse(out), nil
}

// BeginTotpEnrolment is beginTotpEnrolment: a new pending seed, returned once with its otpauth URI (C-03.FR-10).
func (s *Server) BeginTotpEnrolment(ctx context.Context, _ gen.BeginTotpEnrolmentRequestObject) (
	gen.BeginTotpEnrolmentResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.totp.Begin(ctx, id.Session)
	if err != nil {
		return nil, err
	}
	return gen.BeginTotpEnrolment201JSONResponse(gen.TotpEnrolment{Secret: e.Secret, OtpauthUri: e.URI}), nil
}

// ConfirmTotpEnrolment is confirmTotpEnrolment: the first code completes the enrolment and returns the recovery
// codes, shown once; a session that had to enrol becomes active.
func (s *Server) ConfirmTotpEnrolment(ctx context.Context, req gen.ConfirmTotpEnrolmentRequestObject) (
	gen.ConfirmTotpEnrolmentResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	codes, err := s.totp.Confirm(ctx, id.Session, req.Body.Code, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	return gen.ConfirmTotpEnrolment200JSONResponse(gen.TotpRecoveryCodes{Codes: codes}), nil
}

// RegenerateTotpRecoveryCodes is regenerateTotpRecoveryCodes: a current code replaces the unused recovery codes.
func (s *Server) RegenerateTotpRecoveryCodes(ctx context.Context, req gen.RegenerateTotpRecoveryCodesRequestObject) (
	gen.RegenerateTotpRecoveryCodesResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	codes, err := s.totp.RegenerateRecoveryCodes(ctx, id.Session, req.Body.Code, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	return gen.RegenerateTotpRecoveryCodes200JSONResponse(gen.TotpRecoveryCodes{Codes: codes}), nil
}

// RemoveTotp is removeTotp: the current password, a current code or a recovery code removes the caller's TOTP
// (C-03.FR-27); a wrong proof is 401 and changes nothing, and a user without TOTP gets 404.
func (s *Server) RemoveTotp(ctx context.Context, req gen.RemoveTotpRequestObject) (gen.RemoveTotpResponseObject,
	error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	p := totp.Removal{Password: deref(req.Body.Password), TOTPCode: deref(req.Body.TotpCode),
		RecoveryCode: deref(req.Body.RecoveryCode)}
	if p == (totp.Removal{}) {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired,
			"Give the password, a TOTP code or a recovery code.")
	}
	err = s.totp.Remove(ctx, id.Session, p, clientAddress(ctx))
	if errors.Is(err, totp.ErrNotEnrolled) {
		return nil, problem(http.StatusNotFound, typeNotFound, "", "There is no TOTP to remove.")
	}
	if err != nil {
		return nil, err
	}
	return gen.RemoveTotp204Response{}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
