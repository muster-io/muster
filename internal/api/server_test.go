// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	spec "github.com/muster-io/muster/api"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/users"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

var roles = auth.Roles{
	auth.RoleAdmin: {"alert-groups:read", "integrations:read", "organization:write", "service-accounts:read",
		"service-accounts:write", "system-status:read", "users:read", "users:write"},
	auth.RoleResponder: {"alert-groups:acknowledge", "alert-groups:read", "integrations:read"},
	auth.RoleViewer:    {"alert-groups:read", "integrations:read"},
}

// fakeSessions stands for internal/auth: a cookie names a session, and its CSRF token is "csrf-" and the cookie.
type fakeSessions struct {
	sessions  map[string]auth.Session
	cookies   map[int64]string
	signIn    func(auth.SignInRequest) (auth.Session, error)
	authErr   error
	err       error
	changes   []auth.PasswordChange
	signedOut []string
	list      []auth.SessionInfo
	submit    func(auth.Session, auth.Proof) (auth.Session, error)
}

func (f *fakeSessions) SignIn(_ context.Context, req auth.SignInRequest) (auth.Session, error) {
	return f.signIn(req)
}

func (f *fakeSessions) Authenticate(_ context.Context, cookie string) (auth.Session, error) {
	if f.authErr != nil {
		return auth.Session{}, f.authErr
	}
	s, ok := f.sessions[cookie]
	if !ok {
		return auth.Session{}, auth.ErrUnauthenticated
	}
	return s, nil
}

func (f *fakeSessions) Permissions(sess auth.Session) []auth.Permission {
	if sess.State != auth.StateActive {
		return []auth.Permission{}
	}
	return roles.Permissions(sess.User.Role)
}

func (f *fakeSessions) CSRFToken(sess auth.Session) (string, error) {
	return "csrf-" + f.cookies[sess.ID], f.err
}

func (f *fakeSessions) CheckCSRF(sess auth.Session, header string) bool {
	return header == "csrf-"+f.cookies[sess.ID]
}

func (f *fakeSessions) SignOut(_ context.Context, sess auth.Session, _ netip.Addr) error {
	f.signedOut = append(f.signedOut, sess.PublicID)
	return f.err
}

func (f *fakeSessions) SignOutEverywhere(_ context.Context, sess auth.Session, _ netip.Addr) error {
	f.signedOut = append(f.signedOut, "all:"+sess.User.PublicID)
	return f.err
}

func (f *fakeSessions) ListSessions(context.Context, auth.Session) ([]auth.SessionInfo, error) {
	return f.list, f.err
}

func (f *fakeSessions) ChangePassword(_ context.Context, _ auth.Session, c auth.PasswordChange) error {
	f.changes = append(f.changes, c)
	return f.err
}

func (f *fakeSessions) SubmitSecondFactor(_ context.Context, sess auth.Session, p auth.Proof, _ netip.Addr) (
	auth.Session, error) {
	return f.submit(sess, p)
}

func (f *fakeSessions) Roles() auth.Roles { return roles }

// fakeUsers stands for internal/users.
type fakeUsers struct {
	users    map[int64]users.User
	profiles []users.Profile
	err      error
}

func (f *fakeUsers) Get(_ context.Context, id int64) (users.User, error) {
	if f.err != nil {
		return users.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	return u, nil
}

func (f *fakeUsers) UpdateProfile(_ context.Context, id int64, _ audit.Actor, p users.Profile, _ netip.Addr) (
	users.User, error) {
	if err := p.Validate(); err != nil {
		return users.User{}, err
	}
	f.profiles = append(f.profiles, p)
	u := f.users[id]
	u.Name, u.TimeZone, u.Language = p.Name, p.TimeZone, p.Language
	u.Version++
	f.users[id] = u
	return u, nil
}

type testAPI struct {
	srv      *Server
	sessions *fakeSessions
	users    *fakeUsers
	log      *bytes.Buffer
	router   routers.Router
}

func user(id int64, publicID, name, role string) users.User {
	return users.User{ID: id, PublicID: publicID, Login: name + "@example.org", Name: name, Email: name + "@example.org",
		Role: role, Source: "local", Status: "active", HasPassword: true, CreatedAt: t0, Version: 1}
}

func session(id int64, u users.User, state auth.SessionState) auth.Session {
	return auth.Session{ID: id, PublicID: fmt.Sprintf("SNAAAAAAAAAAA%d", id), State: state, Method: "local",
		CreatedAt: t0, LastUsedAt: t0, IdleExpiresAt: t0.Add(12 * time.Hour), ExpiresAt: t0.Add(7 * 24 * time.Hour),
		User: auth.Principal{ID: u.ID, PublicID: u.PublicID, Name: u.Name, Role: u.Role}}
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	admin := user(1, "SRAAAAAAAAAAAA", "admin", auth.RoleAdmin)
	viewer := user(2, "SRBBBBBBBBBBBB", "viewer", auth.RoleViewer)
	fs := &fakeSessions{
		sessions: map[string]auth.Session{
			"admin-cookie":   session(1, admin, auth.StateActive),
			"viewer-cookie":  session(2, viewer, auth.StateActive),
			"limited-cookie": session(3, admin, auth.StateTOTPRequired),
			"enrol-cookie":   session(4, admin, auth.StateTOTPEnrolmentRequired),
		},
		cookies: map[int64]string{1: "admin-cookie", 2: "viewer-cookie", 3: "limited-cookie", 4: "enrol-cookie"},
	}
	fu := &fakeUsers{users: map[int64]users.User{1: admin, 2: viewer}}
	var log bytes.Buffer
	srv, err := New(Config{Sessions: fs, Users: fu, Log: logging.New(&log, logging.LevelInfo), Real: clock.Real{},
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &testAPI{srv: srv, sessions: fs, users: fu, log: &log, router: router}
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("body %q: %v", a.body, err)
	}
	return m
}

func (a answer) code(t *testing.T) string {
	t.Helper()
	c, _ := a.json(t)["code"].(string)
	return c
}

// call sends a request through the API and, for an implemented operation, validates the answer against the
// specification: its status must be declared and its body must match the schema (NFR-13).
func (x *testAPI) call(t *testing.T, method, path, body string, headers ...string) answer {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequestWithContext(t.Context(), method, path, rd)
	r.RemoteAddr = "192.0.2.1:5000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Cookie" {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: headers[i+1], Secure: true, HttpOnly: true}) //nolint:gosec // G124: a request cookie carries no attributes
			continue
		}
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	x.srv.ServeHTTP(w, r)
	a := answer{status: w.Code, header: w.Header(), body: w.Body.Bytes()}
	x.validate(t, method, path, a)
	return a
}

func (x *testAPI) validate(t *testing.T, method, path string, a answer) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	route, params, err := x.router.FindRoute(req)
	// 500 internal and 413 payload-too-large can answer any operation; the specification lists them on none.
	if err != nil || !implemented[goName(route.Operation.OperationID)] || a.status == http.StatusInternalServerError ||
		a.status == http.StatusRequestEntityTooLarge {
		return
	}
	// The YAML of getOpenApiSpec is a string to the schema; its status and content type are still checked.
	excludeBody := route.Operation.OperationID == "getOpenApiSpec"
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}},
		Status: a.status, Header: a.header, Body: io.NopCloser(bytes.NewReader(a.body)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true, ExcludeResponseBody: excludeBody},
	}
	if err := openapi3filter.ValidateResponse(t.Context(), in); err != nil {
		t.Errorf("%s %s answered %d, which the specification does not allow: %v\n%s", method, path, a.status, err,
			a.body)
	}
}

func goName(operationID string) string {
	return strings.ToUpper(operationID[:1]) + operationID[1:]
}

// TestImplementedOperations: every operation the server implements is in the specification.
func TestImplementedOperations(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			found[goName(op.OperationID)] = true
		}
	}
	for name := range implemented {
		if !found[name] {
			t.Errorf("%s is not an operation of the specification", name)
		}
	}
}

// TestXPermission is C-03.FR-2: every operation names its Permission in x-permission — a Permission of the spec's
// enum, a list of them, or a special value — so that the middleware checks Permissions, never Roles.
func TestXPermission(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	var enum []string
	for _, v := range doc.Components.Schemas["Permission"].Value.Enum {
		enum = append(enum, v.(string))
	}
	if len(enum) != 31 {
		t.Errorf("the Permission enum has %d values, want 31", len(enum))
	}
	special := []string{auth.PermissionNone, auth.PermissionAuthenticated, auth.PermissionIntegrationToken}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			perms := xPermission(op)
			if len(perms) == 0 {
				t.Errorf("%s %s has no x-permission", method, path)
			}
			for _, p := range perms {
				if !slices.Contains(enum, p) && !slices.Contains(special, p) {
					t.Errorf("%s %s needs %q, which is not a Permission", method, path, p)
				}
				if slices.Contains(auth.RoleNames, p) {
					t.Errorf("%s %s is checked against a Role", method, path)
				}
			}
		}
	}
	ops := readOperations(doc)
	byID := map[string]*operation{}
	for _, o := range ops {
		byID[o.id] = o
	}
	for id, want := range map[string][3]bool{ // public, sessionOnly, validate
		"getSignInOptions": {true, false, true}, "createSession": {true, false, true},
		"updateMe": {false, true, true}, "getMe": {false, false, true}, "listRoles": {false, false, true},
		"changePassword": {false, true, true}, "deleteMySessions": {false, true, true},
	} {
		o := byID[id]
		if o == nil || o.public != want[0] || o.sessionOnly != want[1] || o.validate != want[2] {
			t.Errorf("%s = %+v, want public, sessionOnly, validate %v", id, o, want)
		}
	}
	if byID["ingestSnapshot"] != nil || byID["getMetrics"] != nil {
		t.Error("an operation of another listener is served on the app listener")
	}
}

// TestPathTemplates: the route_pattern values of the API metrics are the specification's path templates.
func TestPathTemplates(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	got := spec.PathTemplates()
	want := doc.Paths.InMatchingOrder()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("PathTemplates = %v\nwant %v", got, want)
	}
}

// TestProblemCodes: every code this package answers with is catalogued in x-problem-codes.
func TestProblemCodes(t *testing.T) {
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
	has := func(typ, in, code string) bool {
		for _, c := range catalogue {
			if c.Type == typ && c.In == in && slices.ContainsFunc(c.Codes, func(x struct {
				Code string `json:"code"`
			}) bool {
				return x.Code == code
			}) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ typ, code string }{
		{typeUnauthenticated, codeInvalidCredentials}, {typeUnauthenticated, codeSessionExpired},
		{typeForbidden, codeCSRFInvalid}, {typeForbidden, codeSessionRequired}, {typeForbidden, codeTOTPRequired},
		{typeForbidden, codeTOTPEnrolmentRequired}, {typeConflict, codeLocalUserOnly},
	} {
		if !has(c.typ, "code", c.code) {
			t.Errorf("%s %s is not catalogued", c.typ, c.code)
		}
	}
	for _, code := range []string{fieldRequired, fieldInvalidFormat, fieldTooShort, fieldTooLong} {
		if !has(typeValidationFailed, "errors[].code", code) {
			t.Errorf("errors[].code %s is not catalogued", code)
		}
	}
	types, _ := json.Marshal(doc.Components.Schemas["Problem"].Value.Extensions["x-problem-types"])
	for typ := range titles {
		if !strings.Contains(string(types), `"type":"`+typ+`"`) {
			t.Errorf("problem type %s is not in x-problem-types", typ)
		}
	}
}

// TestNotFoundAndNotImplemented is S-010 step 1: an unknown path under /api/v1 is a 404 problem, and an operation no
// story has implemented answers 501 with a problem.
func TestNotFoundAndNotImplemented(t *testing.T) {
	x := newTestAPI(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/does-not-exist"}, {http.MethodPatch, "/api/v1/me"}, {http.MethodGet, "/api/v2/me"},
		{http.MethodGet, "/api/v1"}, {http.MethodGet, "/api/v1/metrics"}, {http.MethodPost, "/api/v1/ingest"},
	} {
		a := x.call(t, c.method, c.path, "")
		if a.status != http.StatusNotFound || a.header.Get("Content-Type") != contentTypeProblem ||
			a.json(t)["type"] != problemBase+"not-found" || a.json(t)["instance"] != c.path {
			t.Errorf("%s %s = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	a := x.call(t, http.MethodGet, "/api/v1/integrations", "", "Cookie", "admin-cookie")
	if a.status != http.StatusNotImplemented || a.json(t)["type"] != problemBase+"not-implemented" {
		t.Errorf("GET /integrations = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/integrations", "")
	if a.status != http.StatusUnauthorized {
		t.Errorf("an unimplemented operation without credentials = %d", a.status)
	}
}

// TestRequestValidation is S-010 step 1: a request that breaks the schema is a 400 validation-failed problem with a
// JSON pointer per field, and the detail never repeats the value.
func TestRequestValidation(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":5,"totp_code":"x"}`)
	if a.status != http.StatusBadRequest || a.json(t)["type"] != problemBase+"validation-failed" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	var p struct {
		Errors []struct{ Pointer, Code string } `json:"errors"`
	}
	_ = json.Unmarshal(a.body, &p)
	if !slices.ContainsFunc(p.Errors, func(e struct{ Pointer, Code string }) bool {
		return e.Pointer == "/password" && e.Code == "required"
	}) || !slices.ContainsFunc(p.Errors, func(e struct{ Pointer, Code string }) bool {
		return e.Pointer == "/login" && e.Code == "invalid_format"
	}) {
		t.Errorf("errors = %+v", p.Errors)
	}
	csrf := []string{"Cookie", "admin-cookie", auth.CSRFHeader, "csrf-admin-cookie"}
	for body, want := range map[string]struct{ pointer, code string }{
		`{"name":""}`:                   {"/name", "too_short"},
		`{"name":"a","language":"de"}`:  {"/language", "invalid_format"},
		`{"name":"a","time_zone":5}`:    {"/time_zone", "invalid_format"},
		`{}`:                            {"/name", "required"},
		`not json`:                      {"", "invalid_format"},
		`{"name":"secret-value-777!!"}`: {},
	} {
		a := x.call(t, http.MethodPut, "/api/v1/me", body, csrf...)
		if want.code == "" {
			if a.status != http.StatusOK {
				t.Errorf("%s = %d %s", body, a.status, a.body)
			}
			continue
		}
		if a.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, a.status, a.body)
			continue
		}
		_ = json.Unmarshal(a.body, &p)
		if len(p.Errors) == 0 || p.Errors[0].Pointer != want.pointer || p.Errors[0].Code != want.code {
			t.Errorf("%s: errors = %+v, want %+v", body, p.Errors, want)
		}
	}
	a = x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"a","password":["hunter2-secret"]}`)
	if a.status != http.StatusBadRequest || bytes.Contains(a.body, []byte("hunter2")) {
		t.Errorf("a wrong password value is echoed: %s", a.body)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/sessions",
		strings.NewReader(`{"login":"a","password":"b"}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	x.srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d", w.Code)
	}
}

// TestValidationMessages covers both forms in which kin-openapi reports schema errors.
func TestValidationMessages(t *testing.T) {
	for reason, want := range map[string][]string{
		`at '': missing properties 'login', 'password'`:   {"/login required", "/password required"},
		`at '/a': missing property 'b'`:                   {"/a/b required"},
		`error at "/x": at '/x': got number, want string`: {"/x invalid_format"},
		`at '/x': minLength: got 0, want 1`:               {"/x too_short"},
		`at '/x': maxLength: got 9, want 1`:               {"/x too_long"},
		`at '': missing properties`:                       {" required"},
		`something else`:                                  {" invalid_format"},
		`at '/x' without the separator`:                   {" invalid_format"},
	} {
		var got []string
		for _, it := range messageItems(reason) {
			got = append(got, it.Pointer+" "+it.Code)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q = %v, want %v", reason, got, want)
		}
	}
	for field, want := range map[string]string{"required": "/name required", "minLength": " too_short",
		"maxItems": " too_long", "enum": " invalid_format"} {
		it := keywordItem(&openapi3.SchemaError{SchemaField: field, Reason: `property "name" is missing`})
		if it.Pointer+" "+it.Code != want {
			t.Errorf("%s = %s %s, want %s", field, it.Pointer, it.Code, want)
		}
	}
	if items := bodyItems(errors.New("eof")); items[0].Code != fieldInvalidFormat {
		t.Errorf("a plain error = %+v", items)
	}
	if items := bodyItems(openapi3filter.ErrInvalidRequired); items[0].Code != fieldRequired {
		t.Errorf("a missing body = %+v", items)
	}
	if p := validationProblem(errors.New("other")); len(p.Errors) != 1 || p.Errors[0].Code != fieldInvalidFormat {
		t.Errorf("an unknown error = %+v", p.Errors)
	}
	if p := validationProblem(openapi3.MultiError{}); len(p.Errors) != 1 {
		t.Errorf("no error = %+v", p.Errors)
	}
	param := &openapi3filter.RequestError{Parameter: &openapi3.Parameter{In: "query", Name: "limit"},
		Err: openapi3filter.ErrInvalidRequired}
	if p := validationProblem(openapi3.MultiError{param}); p.Errors[0].Pointer != "/query/limit" ||
		p.Errors[0].Code != fieldRequired {
		t.Errorf("a parameter = %+v", p.Errors)
	}
	if escapePointer("a/b~c") != "a~1b~0c" {
		t.Error("escapePointer")
	}
}

// TestSecurityOfRequests: no credentials, a bearer token (until C-04), an unknown or expired cookie are 401; an
// expired session clears the cookie.
func TestSecurityOfRequests(t *testing.T) {
	x := newTestAPI(t)
	for name, headers := range map[string][]string{
		"none":    nil,
		"bearer":  {"Authorization", "Bearer mstr_pat_x", "Cookie", "admin-cookie"},
		"unknown": {"Cookie", "nope"},
		"empty":   {"Cookie", ""},
	} {
		a := x.call(t, http.MethodGet, "/api/v1/me", "", headers...)
		if a.status != http.StatusUnauthorized || a.json(t)["type"] != problemBase+"unauthenticated" {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	x.sessions.authErr = auth.ErrSessionExpired
	a := x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "admin-cookie")
	if a.status != http.StatusUnauthorized || a.code(t) != codeSessionExpired ||
		!strings.Contains(a.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("expired = %d %s %v", a.status, a.body, a.header)
	}
	x.sessions.authErr = errors.New("database down")
	a = x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "admin-cookie")
	if a.status != http.StatusInternalServerError || !strings.Contains(x.log.String(), `"event":"api_request_failed"`) ||
		bytes.Contains(a.body, []byte("database down")) {
		t.Errorf("a failed lookup = %d %s", a.status, a.body)
	}
}

// TestCSRF is C-03.FR-9 and C-03.AC-3: a mutating request with the session cookie but without the session's
// X-CSRF-Token is 403 csrf_invalid; with it the request succeeds. Reads need no token.
func TestCSRF(t *testing.T) {
	x := newTestAPI(t)
	for name, token := range map[string]string{"missing": "", "wrong": "csrf-viewer-cookie"} {
		a := x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Admin"}`, "Cookie", "admin-cookie",
			auth.CSRFHeader, token)
		if a.status != http.StatusForbidden || a.code(t) != codeCSRFInvalid {
			t.Errorf("%s token = %d %s", name, a.status, a.body)
		}
		a = x.call(t, http.MethodDelete, "/api/v1/sessions/current", "", "Cookie", "admin-cookie", auth.CSRFHeader,
			token)
		if a.status != http.StatusForbidden || a.code(t) != codeCSRFInvalid {
			t.Errorf("sign out with a %s token = %d", name, a.status)
		}
	}
	a := x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Admin"}`, "Cookie", "admin-cookie", auth.CSRFHeader,
		"csrf-admin-cookie")
	if a.status != http.StatusOK {
		t.Errorf("with the token = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "admin-cookie"); a.status != http.StatusOK {
		t.Errorf("a read without a token = %d", a.status)
	}
}

// TestPermissions is C-03.FR-2: each operation is checked against its x-permission; listRoles needs users:read.
func TestPermissions(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/roles", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusForbidden || a.json(t)["type"] != problemBase+"forbidden" ||
		!strings.Contains(a.json(t)["detail"].(string), "users:read") {
		t.Errorf("viewer = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/roles", "", "Cookie", "admin-cookie")
	if a.status != http.StatusOK {
		t.Fatalf("admin = %d %s", a.status, a.body)
	}
	var list struct {
		Items []struct {
			Name        string
			Permissions []string
		}
	}
	_ = json.Unmarshal(a.body, &list)
	if len(list.Items) != 3 || list.Items[0].Name != "admin" || list.Items[2].Name != "viewer" ||
		len(list.Items[0].Permissions) != 8 || len(list.Items[2].Permissions) != 2 {
		t.Errorf("roles = %+v", list)
	}
	id := &auth.Identity{Permissions: []auth.Permission{"users:read"}}
	for perms, want := range map[string]bool{"users:read": true, "users:write": false,
		"users:write,users:read": true, "authenticated": true, "none": false, "integration-token": false} {
		if got := allowed(id, strings.Split(perms, ",")); got != want {
			t.Errorf("allowed(%s) = %v", perms, got)
		}
	}
	// A token on an operation for the web session only (C-03.FR-27), and a Service account under /me.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/me", nil)
	op := &operation{id: "updateMe", path: "/me", sessionOnly: true, permissions: []string{"authenticated"}}
	pat := &auth.Identity{Transport: audit.TransportAPI, Token: &auth.Token{ID: 1}}
	if p := credentialsAllowed(pat, op); p != errSessionRequired {
		t.Errorf("a token on updateMe = %v", p)
	}
	if p := credentialsAllowed(&auth.Identity{Transport: audit.TransportUI}, op); p != nil {
		t.Errorf("a session on updateMe = %v", p)
	}
	sat := &auth.Identity{Transport: audit.TransportAPI, Token: &auth.Token{ID: 2, ServiceAccount: &auth.Principal{}}}
	read := &operation{id: "getMyTotp", path: "/me/totp", permissions: []string{"authenticated"}}
	if p := credentialsAllowed(sat, read); p != errServiceAccountDenied {
		t.Errorf("a Service account on getMyTotp = %v", p)
	}
	if p := credentialsAllowed(sat, &operation{id: "x", path: "/metadata"}); p != nil {
		t.Errorf("a Service account outside /me = %v", p)
	}
	w := httptest.NewRecorder()
	x.srv.permit(http.NotFoundHandler()).ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestInfoKey{},
		requestInfo{operation: op})))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no identity = %d", w.Code)
	}
}

// TestLimitedSession: a session waiting for its second factor reads itself and signs out; everything else is 403 with
// the state's code.
func TestLimitedSession(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "limited-cookie")
	if a.status != http.StatusForbidden || a.code(t) != codeTOTPRequired {
		t.Errorf("getMe = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "enrol-cookie")
	if a.status != http.StatusForbidden || a.code(t) != codeTOTPEnrolmentRequired {
		t.Errorf("getMe while enrolling = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/sessions/current", "", "Cookie", "limited-cookie")
	if a.status != http.StatusOK || a.json(t)["state"] != "totp_required" ||
		len(a.json(t)["permissions"].([]any)) != 0 {
		t.Errorf("getCurrentSession = %d %s", a.status, a.body)
	}
}

// TestSpec is C-03.FR-23 and C-03.AC-12: the specification is served byte for byte, without credentials.
func TestSpec(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/openapi.yaml", "")
	if a.status != http.StatusOK || !bytes.Equal(a.body, spec.Spec()) ||
		a.header.Get("Content-Type") != "application/yaml" {
		t.Errorf("= %d %s, %d bytes", a.status, a.header.Get("Content-Type"), len(a.body))
	}
}

// TestMetrics is C-02.FR-19 for the API: every request is counted by path template, method and status, and timed.
func TestMetrics(t *testing.T) {
	x := newTestAPI(t)
	ok := metrics.APIRequests.With("/sign-in-options", http.MethodGet, "200")
	unmatched := metrics.APIRequests.With(metrics.RouteUnmatched, metrics.MethodOther, "404")
	before, beforeUnmatched := ok.Get(), unmatched.Get()
	x.call(t, http.MethodGet, "/api/v1/sign-in-options", "")
	x.call(t, "PROPFIND", "/api/v1/nothing", "")
	if ok.Get()-before != 1 || unmatched.Get()-beforeUnmatched != 1 {
		t.Errorf("counted %d and %d", ok.Get()-before, unmatched.Get()-beforeUnmatched)
	}
	w := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(),
		`muster_api_request_duration_seconds_count{route_pattern="/sign-in-options",method="GET"}`) {
		t.Error("the duration is not observed")
	}
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusOK)
	if rec.status != http.StatusTeapot || rec.Unwrap() == nil {
		t.Error("the recorder keeps the first status")
	}
}

// TestRequestHardening: a body over MaxBodyBytes is 413 payload-too-large, and a mutating request a browser marks as
// cross-site is 403 csrf_invalid, the public sign-in included.
func TestRequestHardening(t *testing.T) {
	x := newTestAPI(t)
	big := `{"login":"` + strings.Repeat("a", MaxBodyBytes) + `","password":"x"}`
	a := x.call(t, http.MethodPost, "/api/v1/sessions", big)
	if a.status != http.StatusRequestEntityTooLarge || a.json(t)["type"] != problemBase+"payload-too-large" {
		t.Errorf("a large body = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"a","password":"b"}`, "Sec-Fetch-Site", "cross-site")
	if a.status != http.StatusForbidden || a.code(t) != codeCSRFInvalid {
		t.Errorf("a cross-site sign-in = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/sign-in-options", "", "Sec-Fetch-Site", "cross-site"); a.status != 200 {
		t.Errorf("a cross-site read = %d", a.status)
	}
}
