// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways"
)

// fakeGatewayContacts stands for internal/gateways: it records who asked for what and answers err when set.
type fakeGatewayContacts struct {
	list  []gateways.Contact
	by    []gateways.Requester
	calls []string
	code  string
	err   error
}

func (f *fakeGatewayContacts) record(r gateways.Requester, call string) error {
	f.by, f.calls = append(f.by, r), append(f.calls, call)
	return f.err
}

func (f *fakeGatewayContacts) List(_ context.Context, userID int64) ([]gateways.Contact, error) {
	f.calls = append(f.calls, "list "+strconv.FormatInt(userID, 10))
	return f.list, f.err
}

func (f *fakeGatewayContacts) ListOfUser(_ context.Context, user string) ([]gateways.Contact, error) {
	f.calls = append(f.calls, "list-of "+user)
	return f.list, f.err
}

func (f *fakeGatewayContacts) Create(_ context.Context, r gateways.Requester, conn, contact string) (
	gateways.Contact, error) {
	return f.list[1], f.record(r, "create "+conn+" "+contact)
}

func (f *fakeGatewayContacts) SendCode(_ context.Context, r gateways.Requester, id string) (gateways.Contact, error) {
	return f.list[1], f.record(r, "code "+id)
}

func (f *fakeGatewayContacts) Confirm(_ context.Context, r gateways.Requester, id, code string) (gateways.Contact,
	error) {
	f.code = code
	return f.list[0], f.record(r, "confirm "+id)
}

func (f *fakeGatewayContacts) Delete(_ context.Context, r gateways.Requester, id string) error {
	return f.record(r, "delete "+id)
}

func (f *fakeGatewayContacts) DeleteOfUser(_ context.Context, r gateways.Requester, user, id string) error {
	return f.record(r, "delete-of "+user+" "+id)
}

func newGatewayContactsAPI(t *testing.T) (*testAPI, *fakeGatewayContacts) {
	t.Helper()
	x := newTestAPI(t)
	x.srv.tokens = newFakeTokens(clock.Real{}, x)
	replaces := "GCAAAAAAAAAAA1"
	f := &fakeGatewayContacts{list: []gateways.Contact{
		{PublicID: "GCAAAAAAAAAAA1", ConnectionPublicID: "CNAAAAAAAAAAG1", ConnectionName: "Example Gateway",
			Contact: "+10000000001", ConfirmedAt: &t0, CreatedAt: t0},
		{PublicID: "GCAAAAAAAAAAA2", ConnectionPublicID: "CNAAAAAAAAAAG1", ConnectionName: "Example Gateway",
			Contact: "+10000000002", Replaces: &replaces, CreatedAt: t0.Add(time.Minute)},
	}}
	x.srv.gatewayContacts = f
	return x, f
}

// TestGatewayContactsMine is C-30.FR-3 and C-03.FR-27: the caller lists, adds, asks for a code, confirms and removes
// their own contacts; the changes take the web session with its CSRF token only — a token gets 403
// session_required, a session without the CSRF token csrf_invalid — and act as the signed-in User in their session;
// a Service account has no contacts.
func TestGatewayContactsMine(t *testing.T) {
	x, f := newGatewayContactsAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me/gateway-contacts", "", "Cookie", "viewer-cookie")
	items, _ := a.json(t)["items"].([]any)
	if a.status != http.StatusOK || len(items) != 2 || f.calls[0] != "list 2" {
		t.Fatalf("= %d %s %v", a.status, a.body, f.calls)
	}
	for i, want := range []string{`{"confirmed":true,"confirmed_at":"2026-10-05T12:00:00Z","connection":` +
		`{"id":"CNAAAAAAAAAAG1","name":"Example Gateway"},"contact":"+10000000001","created_at":` +
		`"2026-10-05T12:00:00Z","id":"GCAAAAAAAAAAA1","replaces":null}`,
		`{"confirmed":false,"confirmed_at":null,"connection":{"id":"CNAAAAAAAAAAG1","name":"Example Gateway"},` +
			`"contact":"+10000000002","created_at":"2026-10-05T12:01:00Z","id":"GCAAAAAAAAAAA2",` +
			`"replaces":"GCAAAAAAAAAAA1"}`} {
		if got := compact(t, items[i]); got != want {
			t.Errorf("item %d = %s", i, got)
		}
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts",
		`{"connection_id":"CNAAAAAAAAAAG1","contact":"+10000000002"}`, adminCSRF...)
	if a.status != http.StatusAccepted || a.json(t)["id"] != "GCAAAAAAAAAAA2" ||
		f.calls[1] != "create CNAAAAAAAAAAG1 +10000000002" {
		t.Fatalf("create = %d %s %v", a.status, a.body, f.calls)
	}
	if r := f.by[0]; r.UserID != 1 || r.UserPublicID != "SRAAAAAAAAAAAA" || r.SessionID != 1 ||
		r.Address.String() != "192.0.2.1" {
		t.Errorf("requester %+v", r)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/code", "", adminCSRF...)
	if a.status != http.StatusAccepted || f.calls[2] != "code GCAAAAAAAAAAA2" {
		t.Errorf("code = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/confirmation", `{"code":"123456"}`,
		adminCSRF...)
	if a.status != http.StatusOK || a.json(t)["confirmed"] != true || f.code != "123456" ||
		f.calls[3] != "confirm GCAAAAAAAAAAA2" {
		t.Errorf("confirm = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/confirmation", `{}`, adminCSRF...)
	if a.status != http.StatusBadRequest || len(f.calls) != 4 {
		t.Errorf("confirm without a code = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodDelete, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA1", "", adminCSRF...)
	if a.status != http.StatusNoContent || f.calls[4] != "delete GCAAAAAAAAAAA1" {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	calls := len(f.calls)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/gateway-contacts", `{"connection_id":"CNAAAAAAAAAAG1","contact":"+1"}`},
		{http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/code", ""},
		{http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/confirmation", `{"code":"123456"}`},
		{http.MethodDelete, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2", ""},
	} {
		for _, token := range []string{fullToken, readOnlyToken, serviceToken} {
			a := x.call(t, c.method, c.path, c.body, "Authorization", "Bearer "+token)
			if a.status != http.StatusForbidden || a.code(t) != codeSessionRequired {
				t.Errorf("%s %s with %s = %d %s", c.method, c.path, token, a.status, a.body)
			}
		}
		if a := x.call(t, c.method, c.path, c.body, "Cookie", "admin-cookie"); a.status != http.StatusForbidden ||
			a.code(t) != codeCSRFInvalid {
			t.Errorf("%s %s without the CSRF token = %d", c.method, c.path, a.status)
		}
	}
	if len(f.calls) != calls {
		t.Errorf("refused calls reached the contacts: %v", f.calls[calls:])
	}
	if a := x.call(t, http.MethodGet, "/api/v1/me/gateway-contacts", "", "Authorization", "Bearer "+serviceToken); a.
		status != http.StatusForbidden || a.code(t) != codeServiceAccountDenied {
		t.Errorf("a Service account = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/me/gateway-contacts", "", "Authorization", "Bearer "+readOnlyToken); a.
		status != http.StatusOK {
		t.Errorf("a Personal access token lists = %d %s", a.status, a.body)
	}
}

// TestGatewayContactsAdmin is C-18.FR-6 revised and C-30.AC-2: an Admin lists and removes a User's contacts with
// users:read and users:write, by session or token, but never sets one: createUserGatewayContact always answers 403
// forbidden with the detail that only the person sets their contact, whatever the body, and changes nothing; an
// Admin's own contact is removed from the web session only.
func TestGatewayContactsAdmin(t *testing.T) {
	x, f := newGatewayContactsAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts", "", "Authorization",
		"Bearer "+readOnlyToken)
	if a.status != http.StatusOK || f.calls[0] != "list-of SRBBBBBBBBBBBB" {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts", "", "Cookie",
		"viewer-cookie"); a.status != http.StatusForbidden {
		t.Errorf("a Viewer's list = %d", a.status)
	}
	calls := len(f.calls)
	for _, c := range []struct {
		body    string
		headers []string
	}{
		{`{"contact":"+1"}`, []string{"Authorization", "Bearer " + fullToken}},
		{`{"connection_id":"CNAAAAAAAAAAG1","contact":"+10000000003"}`, adminCSRF},
		{"", adminCSRF},
		{`{"unknown":[1,2]}`, []string{"Authorization", "Bearer " + fullToken}},
	} {
		a := x.call(t, http.MethodPost, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts", c.body, c.headers...)
		if a.status != http.StatusForbidden || a.json(t)["type"] != problemBase+typeForbidden ||
			a.json(t)["detail"] != detailOnlyThePerson {
			t.Errorf("set a contact with %q = %d %s", c.body, a.status, a.body)
		}
	}
	if len(f.calls) != calls {
		t.Errorf("an Admin's attempt reached the contacts: %v", f.calls[calls:])
	}
	a = x.call(t, http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts/GCAAAAAAAAAAA1", "",
		"Authorization", "Bearer "+fullToken)
	if a.status != http.StatusNoContent || f.calls[len(f.calls)-1] != "delete-of SRBBBBBBBBBBBB GCAAAAAAAAAAA1" {
		t.Errorf("delete = %d %s %v", a.status, a.body, f.calls)
	}
	if f.by[len(f.by)-1].SessionID != 0 {
		t.Errorf("a token's requester has a session: %+v", f.by[len(f.by)-1])
	}
	a = x.call(t, http.MethodDelete, "/api/v1/users/SRAAAAAAAAAAAA/gateway-contacts/GCAAAAAAAAAAA1", "",
		"Authorization", "Bearer "+fullToken)
	if a.status != http.StatusForbidden || a.code(t) != codeSessionRequired {
		t.Errorf("an Admin's own contact by token = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodDelete, "/api/v1/users/SRAAAAAAAAAAAA/gateway-contacts/GCAAAAAAAAAAA1", "",
		adminCSRF...)
	if a.status != http.StatusNoContent {
		t.Errorf("an Admin's own contact by session = %d %s", a.status, a.body)
	}
}

// TestGatewayContactsProblems maps the errors of internal/gateways: 422 unknown_id, invalid_format and
// contact_code_invalid with attempts_remaining, 429 contact_code_requests_exceeded with Retry-After, 502
// gateway_refused with the class, 503 with Retry-After for the interactive budget, 404, and 500 for anything else;
// no answer repeats the contact or the code.
func TestGatewayContactsProblems(t *testing.T) {
	x, f := newGatewayContactsAPI(t)
	two := 2
	create := func(found bool) answer {
		if found {
			return x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/code", "", adminCSRF...)
		}
		return x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts",
			`{"connection_id":"CNAAAAAAAAAAG1","contact":"+10000000002"}`, adminCSRF...)
	}
	for _, c := range []struct {
		err    error
		status int
		want   []string
		retry  string
	}{
		{&gateways.FieldError{Pointer: "/connection_id", Code: "unknown_id", Detail: "x"}, 422,
			[]string{`"pointer":"/connection_id"`, `"code":"unknown_id"`}, ""},
		{&gateways.FieldError{Pointer: "/code", Code: "contact_code_invalid", Detail: "x", AttemptsRemaining: &two},
			422, []string{`"code":"contact_code_invalid"`, `"attempts_remaining":2`}, ""},
		{&gateways.RateLimitedError{RetryAfter: 90 * time.Second}, 429,
			[]string{`"code":"contact_code_requests_exceeded"`, `"retry_after_seconds":90`}, "90"},
		{&gateways.RefusedError{Class: "invalid"}, 502,
			[]string{`"type":"` + problemBase + `gateway-refused"`, `"code":"gateway_refused"`, `"gateway_class":"invalid"`,
				`"title":"Notification gateway refused"`}, ""},
		{&delivery.LimitedError{RetryAfter: 3 * time.Second}, 503, []string{"interactive-budget-exhausted"}, "3"},
		{gateways.ErrNotFound, 404, []string{"No such contact."}, ""},
		{gateways.ErrUserNotFound, 404, []string{"No such user."}, ""},
		{errors.New("database down +10000000002"), 500, []string{"internal"}, ""},
	} {
		f.err = c.err
		a := create(c.status == http.StatusNotFound)
		for _, w := range c.want {
			if a.status != c.status || !strings.Contains(string(a.body), w) {
				t.Errorf("%v = %d %s, want %d with %s", c.err, a.status, a.body, c.status, w)
			}
		}
		if got := a.header.Get("Retry-After"); got != c.retry {
			t.Errorf("%v: Retry-After %q", c.err, got)
		}
		if strings.Contains(string(a.body), "+10000000002") {
			t.Errorf("%v: the answer repeats the contact: %s", c.err, a.body)
		}
	}
	f.err = gateways.ErrUserNotFound
	if a := x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts", "", adminCSRF...); a.status !=
		http.StatusNotFound {
		t.Errorf("an unknown User = %d", a.status)
	}
	f.err = errors.New("database down")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/me/gateway-contacts", ""},
		{http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/code", ""},
		{http.MethodPost, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2/confirmation", `{"code":"1"}`},
		{http.MethodDelete, "/api/v1/me/gateway-contacts/GCAAAAAAAAAAA2", ""},
		{http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/gateway-contacts/GCAAAAAAAAAAA2", ""},
	} {
		if a := x.call(t, c.method, c.path, c.body, adminCSRF...); a.status != http.StatusInternalServerError {
			t.Errorf("a failed %s %s = %d", c.method, c.path, a.status)
		}
	}
	f.err = nil
	for _, body := range []string{`{"connection_id":"CNAAAAAAAAAAG1","contact":""}`,
		`{"connection_id":"CNAAAAAAAAAAG1","contact":"` + strings.Repeat("1", 201) + `"}`} {
		a := x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts", body, adminCSRF...)
		if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/contact"`) ||
			!strings.Contains(string(a.body), `"code":"invalid_format"`) {
			t.Errorf("contact %d characters = %d %s", len(body), a.status, a.body)
		}
	}
	if a := x.call(t, http.MethodPost, "/api/v1/me/gateway-contacts", `{"contact":"+1"}`, adminCSRF...); a.status !=
		http.StatusBadRequest {
		t.Errorf("without connection_id = %d %s", a.status, a.body)
	}
}

// compact is v as JSON, with its keys sorted.
func compact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestProblemCodesOfGatewayContacts: the problem codes of the contacts at Notification gateways are catalogued in
// x-problem-codes, and gateway-refused in x-problem-types.
func TestProblemCodesOfGatewayContacts(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Components.Schemas["Problem"].Value.Extensions["x-problem-codes"])
	var catalogue []struct {
		Type  string `json:"type"`
		In    string `json:"in"`
		Codes []struct {
			Code string `json:"code"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range catalogue {
		for _, code := range c.Codes {
			found[c.Type+" "+c.In+" "+code.Code] = true
		}
	}
	for _, c := range []string{typeRateLimited + " code " + codeContactCodeRequestsExceeded,
		typeGatewayRefused + " code " + codeGatewayRefused,
		typeValidationFailed + " errors[].code " + gateways.CodeContactCodeInvalid,
		typeValidationFailed + " errors[].code " + gateways.CodeUnknownID,
		typeValidationFailed + " errors[].code " + gateways.CodeInvalidFormat} {
		if !found[c] {
			t.Errorf("%s is not catalogued", c)
		}
	}
}
