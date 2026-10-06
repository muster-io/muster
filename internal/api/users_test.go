// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/users"
)

// fakeAdmin stands for the administration of internal/users; err answers every call.
type fakeAdmin struct {
	users    map[string]users.User
	filter   users.ListFilter
	page     users.Page
	created  []users.NewUser
	changes  []users.Changes
	versions []*int64
	by       []users.Requester
	setups   map[string]error
	err      error
}

func (f *fakeAdmin) List(_ context.Context, fl users.ListFilter) (users.Page, error) {
	f.filter = fl
	return f.page, f.err
}

func (f *fakeAdmin) Get(_ context.Context, id string) (users.User, error) {
	if f.err != nil {
		return users.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	return u, nil
}

func (f *fakeAdmin) Create(_ context.Context, r users.Requester, n users.NewUser) (users.User, users.SetupLink, error) {
	f.by, f.created = append(f.by, r), append(f.created, n)
	if f.err != nil {
		return users.User{}, users.SetupLink{}, f.err
	}
	u := user(5, "SRCCCCCCCCCCCC", n.Name, n.Role)
	u.Login, u.HasPassword = n.Login, false
	return u, setupLink, nil
}

func (f *fakeAdmin) Update(_ context.Context, r users.Requester, id string, version *int64, c users.Changes) (
	users.User, error) {
	f.by, f.changes, f.versions = append(f.by, r), append(f.changes, c), append(f.versions, version)
	u, err := f.Get(context.Background(), id) //nolint:usetesting // a fake of a domain call
	if err != nil {
		return users.User{}, err
	}
	u.Name, u.Role = c.Name, c.Role
	u.Version++
	return u, nil
}

func (f *fakeAdmin) Disable(_ context.Context, r users.Requester, id string) (users.User, error) {
	return f.status(r, id, users.StatusDisabled)
}

func (f *fakeAdmin) Enable(_ context.Context, r users.Requester, id string) (users.User, error) {
	return f.status(r, id, users.StatusActive)
}

func (f *fakeAdmin) status(r users.Requester, id, status string) (users.User, error) {
	f.by = append(f.by, r)
	u, err := f.Get(context.Background(), id) //nolint:usetesting // as above
	u.Status = status
	return u, err
}

func (f *fakeAdmin) Delete(_ context.Context, r users.Requester, id string, version *int64) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	_, err := f.Get(context.Background(), id) //nolint:usetesting // as above
	return err
}

func (f *fakeAdmin) CreateSetupLink(_ context.Context, r users.Requester, id string) (users.SetupLink, error) {
	f.by = append(f.by, r)
	_, err := f.Get(context.Background(), id) //nolint:usetesting // as above
	return setupLink, err
}

func (f *fakeAdmin) CompleteSetup(_ context.Context, token, password string, _ netip.Addr) error {
	if err, ok := f.setups[token]; ok {
		return err
	}
	if len(password) < auth.PasswordMinLength {
		return &users.FieldError{Pointer: "/password", Code: "too_short", Detail: "too short"}
	}
	return users.ErrLinkNotFound
}

var setupLink = users.SetupLink{URL: "http://localhost:8080/password-setup#token=abc", ExpiresAt: t0.Add(24 * time.Hour)}

const (
	adminCookie  = "admin-cookie"
	viewerCookie = "viewer-cookie"
	bobPublicID  = "SRBBBBBBBBBBBB"
)

func newAdminAPI(t *testing.T) (*testAPI, *fakeAdmin) {
	t.Helper()
	x := newTestAPI(t)
	fa := &fakeAdmin{users: map[string]users.User{bobPublicID: x.users.users[2]}, setups: map[string]error{}}
	x.srv.admin = fa
	return x, fa
}

// mutate sends a mutating request with the session cookie and its CSRF token.
func (x *testAPI) mutate(t *testing.T, cookie, method, path, body string, headers ...string) answer {
	t.Helper()
	return x.call(t, method, path, body, append([]string{"Cookie", cookie, "X-CSRF-Token", "csrf-" + cookie},
		headers...)...)
}

// TestListUsers: the filters reach the domain, and next_cursor reads back as the position after the last user.
func TestListUsers(t *testing.T) {
	x, fa := newAdminAPI(t)
	fa.page = users.Page{Users: []users.User{x.users.users[1]}, Next: &users.Cursor{Name: "admin", ID: 1}}
	a := x.call(t, http.MethodGet, "/api/v1/users?q=ad&role=admin&status=active&source=local&limit=1", "",
		"Cookie", adminCookie)
	if a.status != http.StatusOK || fa.filter.Q != "ad" || fa.filter.Role != "admin" || fa.filter.Status != "active" ||
		fa.filter.Source != "local" || fa.filter.Limit != 1 || fa.filter.After != nil {
		t.Fatalf("= %d %s, filter %+v", a.status, a.body, fa.filter)
	}
	cursor, _ := a.json(t)["next_cursor"].(string)
	fa.page = users.Page{Users: []users.User{}}
	a = x.call(t, http.MethodGet, "/api/v1/users?cursor="+cursor, "", "Cookie", adminCookie)
	if a.status != http.StatusOK || fa.filter.Limit != 50 || fa.filter.After == nil ||
		*fa.filter.After != (users.Cursor{Name: "admin", ID: 1}) || a.json(t)["next_cursor"] != nil {
		t.Errorf("second page = %d %s, filter %+v", a.status, a.body, fa.filter)
	}
	for _, c := range []string{"!!", "e30", encodeCursor("audit-log", auditKey{ID: 1}), "bnVsbA"} {
		a = x.call(t, http.MethodGet, "/api/v1/users?cursor="+c, "", "Cookie", adminCookie)
		if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), `"invalid_cursor"`) {
			t.Errorf("cursor %q = %d %s", c, a.status, a.body)
		}
	}
	if a = x.call(t, http.MethodGet, "/api/v1/users?limit=501", "", "Cookie", adminCookie); a.status != http.StatusBadRequest {
		t.Errorf("limit 501 = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/users", "", "Cookie", viewerCookie); a.status != http.StatusForbidden {
		t.Errorf("a viewer lists users: %d", a.status)
	}
	fa.err = errors.New("boom")
	if a = x.call(t, http.MethodGet, "/api/v1/users", "", "Cookie", adminCookie); a.status != http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
}

// TestCreateUser is C-03.FR-3 and C-03.AC-19 at the API: 201 with the user, the link, ETag and Location; 409
// name_taken; 422 for an invalid field; 403 without users:write.
func TestCreateUser(t *testing.T) {
	x, fa := newAdminAPI(t)
	a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users",
		`{"name":"Alice Smith","login":"Alice.Smith","role":"responder","email":"alice@example.org"}`)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/users/SRCCCCCCCCCCCC" {
		t.Fatalf("= %d %v %s", a.status, a.header, a.body)
	}
	var body struct {
		User struct {
			Login string `json:"login"`
			Etag  string `json:"etag"`
		} `json:"user"`
		Link struct {
			URL string `json:"url"`
		} `json:"password_setup_link"`
	}
	_ = json.Unmarshal(a.body, &body)
	if body.User.Login != "Alice.Smith" || body.User.Etag != `"1"` || body.Link.URL != setupLink.URL {
		t.Errorf("body %s", a.body)
	}
	if n := fa.created[0]; n.Email == nil || *n.Email != "alice@example.org" || n.Role != "responder" ||
		fa.by[0].Actor.ID != 1 || fa.by[0].Transport != audit.TransportUI || !fa.by[0].Address.IsValid() {
		t.Errorf("new user %+v by %+v", n, fa.by[0])
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", `{"name":"B","login":"b","role":"viewer","email":null}`); a.status != http.StatusCreated || fa.created[1].Email != nil {
		t.Errorf("a null email = %d, %+v", a.status, fa.created[1])
	}
	fa.err = users.ErrNameTaken
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", `{"name":"O","login":"ALICE.SMITH","role":"viewer"}`); a.status != http.StatusConflict || a.code(t) != codeNameTaken {
		t.Errorf("name taken = %d %s", a.status, a.body)
	}
	fa.err = &users.FieldError{Pointer: "/login", Code: "invalid_format", Detail: "x"}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", `{"name":"O","login":"a b","role":"viewer"}`); a.status != http.StatusUnprocessableEntity {
		t.Errorf("an invalid login = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", `{"name":"O","role":"viewer"}`); a.status != http.StatusBadRequest {
		t.Errorf("no login = %d", a.status)
	}
	if a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/users", `{"name":"O","login":"o","role":"viewer"}`); a.status != http.StatusForbidden {
		t.Errorf("a viewer creates a user: %d", a.status)
	}
	if a = x.call(t, http.MethodPost, "/api/v1/users", `{"name":"O","login":"o","role":"viewer"}`, "Cookie", adminCookie); a.status != http.StatusForbidden || a.code(t) != codeCSRFInvalid {
		t.Errorf("without the CSRF token = %d", a.status)
	}
}

func TestGetUser(t *testing.T) {
	x, _ := newAdminAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/users/"+bobPublicID, "", "Cookie", adminCookie)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"1"` || a.json(t)["id"] != bobPublicID {
		t.Errorf("= %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/users/SRZZZZZZZZZZZZ", "", "Cookie", adminCookie); a.status != http.StatusNotFound {
		t.Errorf("an unknown user = %d", a.status)
	}
}

// TestUpdateUser is NFR-13 on users: 428 without If-Match, 412 on a stale one, the version passed on; an omitted
// email keeps its value and null clears it.
func TestUpdateUser(t *testing.T) {
	x, fa := newAdminAPI(t)
	path := "/api/v1/users/" + bobPublicID
	body := `{"name":"Bob","role":"responder"}`
	if a := x.mutate(t, adminCookie, http.MethodPut, path, body); a.status != http.StatusPreconditionRequired ||
		a.json(t)["type"] != problemBase+typePreconditionRequired || len(fa.changes) != 0 {
		t.Errorf("without If-Match = %d %s", a.status, a.body)
	}
	for _, h := range [][]string{{"If-Match", `"1"`}, nil} {
		if a := x.mutate(t, viewerCookie, http.MethodPut, path, body, h...); a.status != http.StatusForbidden {
			t.Errorf("a viewer updates a user with %v: %d", h, a.status)
		}
	}
	if a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", "garbage"); a.status != http.StatusPreconditionFailed {
		t.Errorf("a malformed If-Match = %d", a.status)
	}
	a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `"1"`)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"2"` || *fa.versions[0] != 1 || fa.changes[0].EmailSet {
		t.Fatalf("= %d %s %+v", a.status, a.body, fa.changes)
	}
	x.mutate(t, adminCookie, http.MethodPut, path, `{"name":"Bob","role":"viewer","email":null}`, "If-Match", "*")
	if fa.versions[1] != nil || !fa.changes[1].EmailSet || fa.changes[1].Email != nil {
		t.Errorf("If-Match * with a null email: %v %+v", fa.versions[1], fa.changes[1])
	}
	x.mutate(t, adminCookie, http.MethodPut, path, `{"name":"Bob","role":"viewer","email":"b@example.org"}`,
		"If-Match", `W/"7"`)
	if *fa.versions[2] != 7 || *fa.changes[2].Email != "b@example.org" {
		t.Errorf("a weak tag with an email: %v %+v", *fa.versions[2], fa.changes[2])
	}
	for err, want := range map[error]int{users.ErrVersionMismatch: http.StatusPreconditionFailed,
		users.ErrLastAdmin: http.StatusConflict, users.ErrNotFound: http.StatusNotFound} {
		fa.err = err
		if a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `"1"`); a.status != want {
			t.Errorf("%v = %d %s", err, a.status, a.body)
		}
	}
	fa.err = users.ErrLastAdmin
	if a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `"1"`); a.code(t) != codeLastAdmin {
		t.Errorf("last admin = %s", a.body)
	}
}

// TestUserStatus: disable, enable and delete with their refusals, and the optional If-Match of a delete.
func TestUserStatus(t *testing.T) {
	x, fa := newAdminAPI(t)
	path := "/api/v1/users/" + bobPublicID
	if a := x.mutate(t, adminCookie, http.MethodPost, path+"/disable", ""); a.status != http.StatusOK ||
		a.json(t)["status"] != "disabled" {
		t.Errorf("disable = %d %s", a.status, a.body)
	}
	if a := x.mutate(t, adminCookie, http.MethodPost, path+"/enable", ""); a.status != http.StatusOK ||
		a.json(t)["status"] != "active" {
		t.Errorf("enable = %d %s", a.status, a.body)
	}
	if a := x.mutate(t, adminCookie, http.MethodDelete, path, ""); a.status != http.StatusNoContent || fa.versions[0] != nil {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a := x.mutate(t, adminCookie, http.MethodDelete, path, "", "If-Match", `"4"`); a.status != http.StatusNoContent ||
		*fa.versions[1] != 4 {
		t.Errorf("delete with If-Match = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodDelete, path, "", "If-Match", `4`); a.status != http.StatusPreconditionFailed {
		t.Errorf("delete with a malformed If-Match = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodPost, path+"/password-setup-links", ""); a.status != http.StatusCreated ||
		a.json(t)["url"] != setupLink.URL {
		t.Errorf("link = %d %s", a.status, a.body)
	}
	fa.err = users.ErrLastAdmin
	for _, c := range []struct{ method, path string }{{http.MethodPost, path + "/disable"},
		{http.MethodDelete, path}} {
		if a := x.mutate(t, adminCookie, c.method, c.path, ""); a.status != http.StatusConflict || a.code(t) != codeLastAdmin {
			t.Errorf("%s %s = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	fa.err = users.ErrNotFound
	for _, c := range []struct{ method, path string }{{http.MethodPost, path + "/disable"},
		{http.MethodPost, path + "/enable"}, {http.MethodDelete, path}, {http.MethodPost, path + "/password-setup-links"}} {
		if a := x.mutate(t, adminCookie, c.method, c.path, ""); a.status != http.StatusNotFound {
			t.Errorf("%s %s of an unknown user = %d", c.method, c.path, a.status)
		}
	}
	fa.err = auth.ErrNotLocal
	if a := x.mutate(t, adminCookie, http.MethodPost, path+"/password-setup-links", ""); a.status != http.StatusConflict ||
		a.code(t) != codeLocalUserOnly {
		t.Errorf("a link for an OIDC account = %d %s", a.status, a.body)
	}
	for _, c := range []struct{ method, path string }{{http.MethodPost, path + "/disable"},
		{http.MethodPost, path + "/enable"}, {http.MethodDelete, path}, {http.MethodPost, path + "/password-setup-links"}} {
		if a := x.mutate(t, viewerCookie, c.method, c.path, ""); a.status != http.StatusForbidden {
			t.Errorf("%s %s by a viewer = %d", c.method, c.path, a.status)
		}
	}
}

// TestCompletePasswordSetup is C-03.FR-26 and C-03.AC-17 at the API: public, 204, 404 for an unknown token, 410 with
// link_expired or link_used, 422 too_short.
func TestCompletePasswordSetup(t *testing.T) {
	x, fa := newAdminAPI(t)
	fa.setups = map[string]error{"good": nil, "expired": users.ErrLinkExpired, "used": users.ErrLinkUsed}
	for token, want := range map[string]struct {
		status int
		code   string
	}{
		"good": {http.StatusNoContent, ""}, "expired": {http.StatusGone, codeLinkExpired},
		"used": {http.StatusGone, codeLinkUsed}, "unknown": {http.StatusNotFound, ""},
	} {
		a := x.call(t, http.MethodPost, "/api/v1/password-setups",
			`{"token":"`+token+`","password":"a-good-password-1"}`)
		if a.status != want.status || (want.code != "" && a.code(t) != want.code) {
			t.Errorf("%s = %d %s", token, a.status, a.body)
		}
	}
	a := x.call(t, http.MethodPost, "/api/v1/password-setups", `{"token":"unknown","password":"short"}`)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"too_short"`) {
		t.Errorf("a short password = %d %s", a.status, a.body)
	}
	for _, body := range []string{`{"password":"a-good-password-1"}`, `{"token":"good"}`} {
		if a := x.call(t, http.MethodPost, "/api/v1/password-setups", body); a.status != http.StatusBadRequest {
			t.Errorf("%s = %d", body, a.status)
		}
	}
	if a := x.call(t, http.MethodPost, "/api/v1/password-setups", `{"token":"good","password":"a-good-password-1"}`,
		"Sec-Fetch-Site", "cross-site"); a.status != http.StatusForbidden {
		t.Errorf("a cross-site setup = %d", a.status)
	}
}

// TestHandlersWithoutRequester: the administrative handlers refuse a request that reached them without an identity.
func TestHandlersWithoutRequester(t *testing.T) {
	x, _ := newAdminAPI(t)
	ctx := t.Context()
	var errs []error
	_, err := x.srv.CreateUser(ctx, genCreate())
	errs = append(errs, err)
	_, err = x.srv.UpdateUser(ctx, genUpdate())
	errs = append(errs, err)
	_, err = x.srv.DeleteUser(ctx, genDelete())
	errs = append(errs, err)
	_, err = x.srv.DisableUser(ctx, genDisable())
	errs = append(errs, err)
	_, err = x.srv.EnableUser(ctx, genEnable())
	errs = append(errs, err)
	_, err = x.srv.CreatePasswordSetupLink(ctx, genLink())
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.Is(err, errUnauthenticated) {
			t.Errorf("handler %d: %v", i, err)
		}
	}
	id := &auth.Identity{Session: session(1, x.users.users[1], auth.StateActive), Transport: audit.TransportUI}
	ctx = auth.WithIdentity(ctx, id)
	if _, err := x.srv.CreateUser(ctx, genCreate()); err == nil {
		t.Error("a create without a body")
	}
	if _, err := x.srv.UpdateUser(ctx, genUpdate()); err == nil {
		t.Error("an update without a body")
	}
	if _, err := x.srv.CompletePasswordSetup(ctx, genSetup()); err == nil {
		t.Error("a setup without a body")
	}
}

// TestETagHelpers: the entity tag of a version and the If-Match values it accepts.
func TestETagHelpers(t *testing.T) {
	if etag(12) != `"12"` {
		t.Errorf("etag = %s", etag(12))
	}
	for _, c := range []struct {
		value string
		want  int64
	}{{`"3"`, 3}, {`W/"4"`, 4}, {` "5" `, 5}} {
		if v, err := ifMatch(c.value); err != nil || v == nil || *v != c.want {
			t.Errorf("ifMatch(%q) = %v, %v", c.value, v, err)
		}
	}
	for _, value := range []string{"", "*"} {
		if v, err := ifMatch(value); err != nil || v != nil {
			t.Errorf("ifMatch(%q) = %v, %v", value, v, err)
		}
	}
	for _, value := range []string{"3", `"x"`, `"3`, `"3"x`, `5"`, `"1", "2"`, `"99999999999999999999"`} {
		if _, err := ifMatch(value); !errors.Is(err, errPreconditionFailed) {
			t.Errorf("ifMatch(%q) = %v", value, err)
		}
	}
	if pageSize(nil) != 50 || pageSize(new(0)) != 50 || pageSize(new(7)) != 7 || pageSize(new(900)) != 500 {
		t.Error("pageSize")
	}
}

// TestNoOffset is NFR-13: no query of the repository pages with OFFSET; lists use cursors.
func TestNoOffset(t *testing.T) {
	files, err := filepath.Glob("../*/query.sql")
	if err != nil || len(files) < 5 {
		t.Fatalf("query files %v, %v", files, err)
	}
	offset := regexp.MustCompile(`(?i)\bOFFSET\b`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if offset.Match(b) {
			t.Errorf("%s uses OFFSET", f)
		}
	}
}

// TestProblemCodesOfUsers: the codes of user administration and password setup are catalogued in x-problem-codes.
func TestProblemCodesOfUsers(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Components.Schemas["Problem"].Value.Extensions["x-problem-codes"])
	var catalogue []struct {
		Type  string `json:"type"`
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
			found[c.Type+" "+code.Code] = true
		}
	}
	for _, c := range []string{typeConflict + " " + codeNameTaken, typeConflict + " " + codeLastAdmin,
		typeGone + " " + codeLinkExpired, typeGone + " " + codeLinkUsed, typeValidationFailed + " " + fieldInvalidCursor} {
		if !found[c] {
			t.Errorf("%s is not catalogued", c)
		}
	}
}

func genCreate() gen.CreateUserRequestObject { return gen.CreateUserRequestObject{} }

func genUpdate() gen.UpdateUserRequestObject {
	return gen.UpdateUserRequestObject{UserId: bobPublicID, Params: gen.UpdateUserParams{IfMatch: `"1"`}}
}

func genDelete() gen.DeleteUserRequestObject { return gen.DeleteUserRequestObject{UserId: bobPublicID} }

func genDisable() gen.DisableUserRequestObject {
	return gen.DisableUserRequestObject{UserId: bobPublicID}
}

func genEnable() gen.EnableUserRequestObject { return gen.EnableUserRequestObject{UserId: bobPublicID} }

func genLink() gen.CreatePasswordSetupLinkRequestObject {
	return gen.CreatePasswordSetupLinkRequestObject{UserId: bobPublicID}
}

func genSetup() gen.CompletePasswordSetupRequestObject {
	return gen.CompletePasswordSetupRequestObject{}
}
