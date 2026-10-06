// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/totp"
)

// fakeTOTP stands for internal/totp: the right code is "123456", the right password "pw"; err answers every call.
type fakeTOTP struct {
	enrolled, pending bool
	resets            []string
	resetBy           []audit.Actor
	removals          []totp.Removal
	sessions          []auth.Session
	err               error
}

var recoveryCodes = []string{"aaaaa-bbbbb", "ccccc-ddddd"}

func (f *fakeTOTP) Status(context.Context, int64) (totp.Status, error) {
	st := totp.Status{Enrolled: f.enrolled, Pending: f.pending}
	if f.enrolled {
		at := t0
		st.EnrolledAt, st.RecoveryCodesRemaining = &at, 9
	}
	return st, f.err
}

func (f *fakeTOTP) Begin(_ context.Context, sess auth.Session) (totp.Enrolment, error) {
	f.sessions = append(f.sessions, sess)
	if f.err != nil {
		return totp.Enrolment{}, f.err
	}
	if f.enrolled {
		return totp.Enrolment{}, totp.ErrAlreadyEnrolled
	}
	f.pending = true
	return totp.Enrolment{Secret: "JBSWY3DPEHPK3PXP",
		URI: "otpauth://totp/Muster:admin@example.org?secret=JBSWY3DPEHPK3PXP&issuer=Muster"}, nil
}

func (f *fakeTOTP) Confirm(_ context.Context, sess auth.Session, code string, _ netip.Addr) ([]string, error) {
	f.sessions = append(f.sessions, sess)
	switch {
	case f.err != nil:
		return nil, f.err
	case f.enrolled:
		return nil, totp.ErrAlreadyEnrolled
	case !f.pending:
		return nil, totp.ErrNotStarted
	case code != "123456":
		return nil, auth.ErrInvalidCredentials
	}
	f.enrolled, f.pending = true, false
	return recoveryCodes, nil
}

func (f *fakeTOTP) RegenerateRecoveryCodes(_ context.Context, _ auth.Session, code string, _ netip.Addr) ([]string,
	error) {
	switch {
	case f.err != nil:
		return nil, f.err
	case !f.enrolled:
		return nil, totp.ErrNotEnrolled
	case code != "123456":
		return nil, auth.ErrInvalidCredentials
	}
	return recoveryCodes, nil
}

func (f *fakeTOTP) Remove(_ context.Context, _ auth.Session, p totp.Removal, _ netip.Addr) error {
	f.removals = append(f.removals, p)
	switch {
	case f.err != nil:
		return f.err
	case !f.enrolled:
		return totp.ErrNotEnrolled
	case p.Password != "pw" && p.TOTPCode != "123456":
		return auth.ErrInvalidCredentials
	}
	f.enrolled = false
	return nil
}

func (f *fakeTOTP) Reset(_ context.Context, actor audit.Actor, _ audit.Transport, _ netip.Addr, id string) error {
	f.resets, f.resetBy = append(f.resets, id), append(f.resetBy, actor)
	switch {
	case actor.PublicID == id:
		return totp.ErrOwnTOTP
	case id != bobPublicID:
		return totp.ErrUserNotFound
	}
	return f.err
}

func newTOTPAPI(t *testing.T) (*testAPI, *fakeTOTP) {
	t.Helper()
	x := newTestAPI(t)
	f := &fakeTOTP{}
	x.srv.totp = f
	return x, f
}

// TestTotpEnrolment is C-03.FR-10 through the API: the status, the enrolment with its seed and otpauth URI, the
// confirmation with a first code and the recovery codes, each refusal with its problem.
func TestTotpEnrolment(t *testing.T) {
	x, f := newTOTPAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me/totp", "", "Cookie", adminCookie)
	if a.status != http.StatusOK ||
		strings.TrimSpace(string(a.body)) != `{"enrolled":false,"enrolled_at":null,"enrolment_pending":false,"recovery_codes_remaining":0}` {
		t.Errorf("status = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodPost, "/api/v1/me/totp", "", "Cookie", adminCookie); a.status != http.StatusForbidden ||
		a.code(t) != codeCSRFInvalid {
		t.Errorf("begin without the CSRF token = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/confirmation", `{"code":"123456"}`)
	if a.status != http.StatusConflict || a.code(t) != codeTOTPNotStarted {
		t.Errorf("confirm before begin = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp", "")
	if a.status != http.StatusCreated || a.json(t)["secret"] != "JBSWY3DPEHPK3PXP" ||
		!strings.HasPrefix(a.json(t)["otpauth_uri"].(string), "otpauth://totp/Muster:") {
		t.Errorf("begin = %d %s", a.status, a.body)
	}
	for body, want := range map[string]int{`{}`: http.StatusBadRequest, `{"code":"000000"}`: http.StatusUnauthorized} {
		if a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/confirmation", body); a.status != want {
			t.Errorf("confirm %s = %d %s", body, a.status, a.body)
		}
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/confirmation", `{"code":"123456"}`)
	if a.status != http.StatusOK || len(a.json(t)["codes"].([]any)) != 2 {
		t.Errorf("confirm = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/me/totp", "", "Cookie", adminCookie)
	if a.json(t)["enrolled"] != true || a.json(t)["recovery_codes_remaining"] != 9.0 ||
		a.json(t)["enrolled_at"] != "2026-10-05T12:00:00Z" {
		t.Errorf("status after = %s", a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp", "")
	if a.status != http.StatusConflict || a.code(t) != codeTOTPAlreadyEnrolled {
		t.Errorf("begin when enrolled = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/recovery-codes", `{"code":"123456"}`)
	if a.status != http.StatusOK || len(a.json(t)["codes"].([]any)) != 2 {
		t.Errorf("regenerate = %d %s", a.status, a.body)
	}
	f.enrolled = false
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/recovery-codes", `{"code":"123456"}`)
	if a.status != http.StatusConflict || a.code(t) != codeTOTPNotEnrolled {
		t.Errorf("regenerate without TOTP = %d %s", a.status, a.body)
	}
	f.err = errors.New("db down")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/me/totp", ""}, {http.MethodPost, "/api/v1/me/totp", ""},
		{http.MethodPost, "/api/v1/me/totp/confirmation", `{"code":"1"}`},
		{http.MethodPost, "/api/v1/me/totp/recovery-codes", `{"code":"1"}`},
		{http.MethodPost, "/api/v1/me/totp/removal", `{"password":"pw"}`},
	} {
		if a := x.mutate(t, adminCookie, c.method, c.path, c.body); a.status != http.StatusInternalServerError {
			t.Errorf("%s %s with the store down = %d", c.method, c.path, a.status)
		}
	}
}

// TestTotpEnrolmentRequired is C-03.FR-10 and FR-24: a session that must enrol may read its TOTP, begin and confirm,
// and nothing else; the confirmation is given its session.
func TestTotpEnrolmentRequired(t *testing.T) {
	x, f := newTOTPAPI(t)
	if a := x.call(t, http.MethodGet, "/api/v1/me/totp", "", "Cookie", "enrol-cookie"); a.status != http.StatusOK {
		t.Errorf("getMyTotp = %d %s", a.status, a.body)
	}
	if a := x.mutate(t, "enrol-cookie", http.MethodPost, "/api/v1/me/totp", ""); a.status != http.StatusCreated {
		t.Errorf("begin = %d %s", a.status, a.body)
	}
	a := x.mutate(t, "enrol-cookie", http.MethodPost, "/api/v1/me/totp/confirmation", `{"code":"123456"}`)
	if a.status != http.StatusOK || f.sessions[len(f.sessions)-1].State != auth.StateTOTPEnrolmentRequired {
		t.Errorf("confirm = %d %s, session %+v", a.status, a.body, f.sessions)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/totp/removal", `{"password":"pw"}`},
		{http.MethodPost, "/api/v1/me/totp/recovery-codes", `{"code":"123456"}`},
		{http.MethodPost, "/api/v1/sessions/current/totp", `{"totp_code":"123456"}`},
		{http.MethodGet, "/api/v1/organization", ""}, {http.MethodGet, "/api/v1/system-notices", ""},
		{http.MethodGet, "/api/v1/live-updates", ""},
	} {
		a := x.mutate(t, "enrol-cookie", c.method, c.path, c.body)
		if a.status != http.StatusForbidden || a.code(t) != codeTOTPEnrolmentRequired {
			t.Errorf("%s %s = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	for _, path := range []string{"/api/v1/me/totp", "/api/v1/organization", "/api/v1/system-notices"} {
		a := x.call(t, http.MethodGet, path, "", "Cookie", "limited-cookie")
		if a.status != http.StatusForbidden || a.code(t) != codeTOTPRequired {
			t.Errorf("%s while the code is due = %d %s", path, a.status, a.body)
		}
	}
}

// TestRemoveTotp is C-03.FR-27 and C-03.AC-15 through the API: a wrong proof is 401 and keeps TOTP, the right one
// removes it; without TOTP the answer is 404; an empty proof is 400.
func TestRemoveTotp(t *testing.T) {
	x, f := newTOTPAPI(t)
	f.enrolled = true
	for body, want := range map[string]int{
		`{}`: http.StatusBadRequest, `{"password":""}`: http.StatusBadRequest,
		`{"password":"wrong-password"}`: http.StatusUnauthorized, `{"totp_code":"000000"}`: http.StatusUnauthorized,
	} {
		a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/removal", body)
		if a.status != want || (want == http.StatusUnauthorized && a.code(t) != codeInvalidCredentials) {
			t.Errorf("%s = %d %s", body, a.status, a.body)
		}
	}
	if !f.enrolled {
		t.Fatal("a wrong proof removed TOTP")
	}
	a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/removal",
		`{"password":"pw","recovery_code":"aaaaa-bbbbb"}`)
	if a.status != http.StatusNoContent || f.enrolled {
		t.Errorf("the right password = %d %s", a.status, a.body)
	}
	last := f.removals[len(f.removals)-1]
	if last.Password != "pw" || last.RecoveryCode != "aaaaa-bbbbb" {
		t.Errorf("removal = %+v", last)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/totp/removal", `{"password":"pw"}`)
	if a.status != http.StatusNotFound {
		t.Errorf("without TOTP = %d %s", a.status, a.body)
	}
}

// TestSessionSecondStep is C-03.FR-24 and C-03.AC-13 through the API: createSession passes the codes on;
// submitSessionTotp completes a session that waits for its code, refuses a wrong one with 401 and a session that
// waits for nothing with 409 totp_not_pending.
func TestSessionSecondStep(t *testing.T) {
	x, _ := newTOTPAPI(t)
	var got auth.SignInRequest
	x.sessions.signIn = func(req auth.SignInRequest) (auth.Session, error) {
		got = req
		return x.sessions.sessions["limited-cookie"], nil
	}
	a := x.call(t, http.MethodPost, "/api/v1/sessions",
		`{"login":"admin@example.org","password":"pw","totp_code":"123456","recovery_code":"aaaaa-bbbbb"}`)
	if a.status != http.StatusCreated || a.json(t)["state"] != "totp_required" ||
		got.Proof != (auth.Proof{TOTPCode: "123456", RecoveryCode: "aaaaa-bbbbb"}) {
		t.Errorf("createSession = %d %s, request %+v", a.status, a.body, got)
	}
	x.sessions.submit = func(sess auth.Session, p auth.Proof) (auth.Session, error) {
		switch {
		case sess.State != auth.StateTOTPRequired:
			return auth.Session{}, auth.ErrTOTPNotPending
		case p.TOTPCode != "123456" && p.RecoveryCode != "aaaaa-bbbbb":
			return auth.Session{}, auth.ErrInvalidCredentials
		}
		sess.State = auth.StateActive
		return sess, nil
	}
	for body, want := range map[string]int{
		`{}`: http.StatusBadRequest, `{"totp_code":""}`: http.StatusBadRequest,
		`{"totp_code":"1","recovery_code":"2"}`: http.StatusBadRequest, `{"totp_code":"000000"}`: http.StatusUnauthorized,
	} {
		if a := x.mutate(t, "limited-cookie", http.MethodPost, "/api/v1/sessions/current/totp", body); a.status != want {
			t.Errorf("%s = %d %s", body, a.status, a.body)
		}
	}
	for _, body := range []string{`{"totp_code":"123456"}`, `{"recovery_code":"aaaaa-bbbbb"}`} {
		a := x.mutate(t, "limited-cookie", http.MethodPost, "/api/v1/sessions/current/totp", body)
		if a.status != http.StatusOK || a.json(t)["state"] != "active" {
			t.Errorf("%s = %d %s", body, a.status, a.body)
		}
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/sessions/current/totp", `{"totp_code":"123456"}`)
	if a.status != http.StatusConflict || a.code(t) != codeTOTPNotPending {
		t.Errorf("an active session = %d %s", a.status, a.body)
	}
	x.sessions.submit = func(auth.Session, auth.Proof) (auth.Session, error) {
		return auth.Session{}, &auth.ThrottledError{RetryAfter: 2 * time.Second}
	}
	a = x.mutate(t, "limited-cookie", http.MethodPost, "/api/v1/sessions/current/totp", `{"totp_code":"123456"}`)
	if a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "2" {
		t.Errorf("throttled = %d %s", a.status, a.body)
	}
}

// TestResetUserTotp is C-03.FR-11 through the API: an Admin resets a user's TOTP as the actor of the request; a
// Viewer is refused; an unknown user is 404.
func TestResetUserTotp(t *testing.T) {
	x, f := newTOTPAPI(t)
	a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users/"+bobPublicID+"/reset-totp", "")
	if a.status != http.StatusNoContent || f.resetBy[0].ID != 1 {
		t.Errorf("reset = %d %s, by %+v", a.status, a.body, f.resetBy)
	}
	a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/users/"+bobPublicID+"/reset-totp", "")
	if a.status != http.StatusForbidden || len(f.resets) != 1 {
		t.Errorf("a Viewer = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users/SRCCCCCCCCCCCC/reset-totp", "")
	if a.status != http.StatusNotFound {
		t.Errorf("an unknown user = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users/SRAAAAAAAAAAAA/reset-totp", "")
	if a.status != http.StatusForbidden {
		t.Errorf("an Admin's own TOTP = %d %s", a.status, a.body)
	}
}

// TestTotpHandlersWithoutIdentity: the handlers refuse a request that reached them without an identity or a body.
func TestTotpHandlersWithoutIdentity(t *testing.T) {
	x, _ := newTOTPAPI(t)
	ctx := t.Context()
	calls := map[string]func(context.Context) error{
		"getMyTotp": func(ctx context.Context) error {
			_, err := x.srv.GetMyTotp(ctx, gen.GetMyTotpRequestObject{})
			return err
		},
		"beginTotpEnrolment": func(ctx context.Context) error {
			_, err := x.srv.BeginTotpEnrolment(ctx, gen.BeginTotpEnrolmentRequestObject{})
			return err
		},
		"confirmTotpEnrolment": func(ctx context.Context) error {
			_, err := x.srv.ConfirmTotpEnrolment(ctx, gen.ConfirmTotpEnrolmentRequestObject{})
			return err
		},
		"regenerateTotpRecoveryCodes": func(ctx context.Context) error {
			_, err := x.srv.RegenerateTotpRecoveryCodes(ctx, gen.RegenerateTotpRecoveryCodesRequestObject{})
			return err
		},
		"removeTotp": func(ctx context.Context) error {
			_, err := x.srv.RemoveTotp(ctx, gen.RemoveTotpRequestObject{})
			return err
		},
		"submitSessionTotp": func(ctx context.Context) error {
			_, err := x.srv.SubmitSessionTotp(ctx, gen.SubmitSessionTotpRequestObject{})
			return err
		},
		"resetUserTotp": func(ctx context.Context) error {
			_, err := x.srv.ResetUserTotp(ctx, gen.ResetUserTotpRequestObject{UserId: bobPublicID})
			return err
		},
	}
	for name, call := range calls {
		if err := call(ctx); !errors.Is(err, errUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	id := &auth.Identity{Session: session(1, x.users.users[1], auth.StateActive), Transport: audit.TransportUI}
	ctx = auth.WithIdentity(ctx, id)
	for _, name := range []string{"confirmTotpEnrolment", "regenerateTotpRecoveryCodes", "removeTotp",
		"submitSessionTotp"} {
		if err := calls[name](ctx); err == nil {
			t.Errorf("%s without a body: no error", name)
		}
	}
}
