// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/notificationrules"
)

// fakeNotificationRules stands for internal/notificationrules: it records the calls and answers err when set.
type fakeNotificationRules struct {
	rules    notificationrules.Rules
	calls    []string
	by       []notificationrules.Requester
	versions []*int64
	updates  []notificationrules.Update
	err      error
}

func (f *fakeNotificationRules) Get(_ context.Context, userID int64) (notificationrules.Rules, error) {
	f.calls = append(f.calls, "get "+strconv.FormatInt(userID, 10))
	return f.rules, f.err
}

func (f *fakeNotificationRules) GetOfUser(_ context.Context, user string) (notificationrules.Rules, error) {
	f.calls = append(f.calls, "get-of "+user)
	return f.rules, f.err
}

func (f *fakeNotificationRules) Update(_ context.Context, r notificationrules.Requester, userID int64,
	version *int64, in notificationrules.Update) (notificationrules.Rules, error) {
	f.calls, f.by = append(f.calls, "update "+strconv.FormatInt(userID, 10)), append(f.by, r)
	f.versions, f.updates = append(f.versions, version), append(f.updates, in)
	return f.rules, f.err
}

func (f *fakeNotificationRules) UpdateOfUser(_ context.Context, r notificationrules.Requester, user string,
	version *int64, in notificationrules.Update) (notificationrules.Rules, error) {
	f.calls, f.by = append(f.calls, "update-of "+user), append(f.by, r)
	f.versions, f.updates = append(f.versions, version), append(f.updates, in)
	return f.rules, f.err
}

func (f *fakeNotificationRules) Reset(_ context.Context, r notificationrules.Requester, userID int64) (
	notificationrules.Rules, error) {
	f.calls, f.by = append(f.calls, "reset "+strconv.FormatInt(userID, 10)), append(f.by, r)
	return f.rules, f.err
}

func newNotificationRulesAPI(t *testing.T) (*testAPI, *fakeNotificationRules) {
	t.Helper()
	x := newTestAPI(t)
	x.srv.tokens = newFakeTokens(clock.Real{}, x)
	responder := user(5, "SRCCCCCCCCCCCC", "responder", auth.RoleResponder)
	x.sessions.sessions["responder-cookie"] = session(5, responder, auth.StateActive)
	x.sessions.cookies[5] = "responder-cookie"
	x.users.users[5] = responder
	f := &fakeNotificationRules{rules: notificationrules.Rules{UserPublicID: "SRAAAAAAAAAAAA",
		State: notificationrules.StateCall, Gateway: &notificationrules.GatewayRef{PublicID: "CNAAAAAAAAAAG1",
			Name: "Example Gateway"}, Version: 4,
		Rules: notificationrules.Describe(notificationrules.Set{
			Normal: []notificationrules.Action{{Kind: "mention"}},
			Important: []notificationrules.Action{{Kind: "call", GatewayID: "CNAAAAAAAAAAG1"}, {Kind: "mention"},
				{Kind: "wait", WaitSeconds: 300}, {Kind: "call", GatewayID: "CNAAAAAAAAAAG2"}}},
			notificationrules.Gateways{{ID: 1, PublicID: "CNAAAAAAAAAAG1", Name: "Example Gateway", Live: true,
				Contact: true}}, true)}}
	x.srv.notificationRules = f
	return x, f
}

// TestNotificationRulesMine is C-31.FR-4, C-31.AC-3 and C-03.FR-27: the caller reads, changes and resets their own
// rules from the web session — a token gets 403 session_required, a change without the CSRF token csrf_invalid, one
// without If-Match 428 — with If-Match passed as the version; a read shows each call's gateway and whether it is
// available; a body's read-only fields are ignored.
func TestNotificationRulesMine(t *testing.T) {
	x, f := newNotificationRulesAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me/notification-rules", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || f.calls[0] != "get 2" {
		t.Fatalf("get = %d %s %v", a.status, a.body, f.calls)
	}
	if got := compact(t, a.json(t)); got != `{"etag":"\"4\"","gateway":{"id":"CNAAAAAAAAAAG1","name":`+
		`"Example Gateway"},"important":[{"available":true,"gateway":{"id":"CNAAAAAAAAAAG1","name":"Example Gateway"},`+
		`"gateway_id":"CNAAAAAAAAAAG1","kind":"call","unavailable_reason":null},{"available":true,"gateway":null,`+
		`"kind":"mention","unavailable_reason":null},{"available":true,"gateway":null,"kind":"wait",`+
		`"unavailable_reason":null,"wait_seconds":300},{"available":false,"gateway":null,"gateway_id":`+
		`"CNAAAAAAAAAAG2","kind":"call","unavailable_reason":"gateway_deleted"}],"normal":[{"available":true,`+
		`"gateway":null,"kind":"mention","unavailable_reason":null}],"state":"call"}` {
		t.Errorf("the rules = %s", got)
	}
	a = x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"custom","gateway_id":"CNAAAAAAAAAAG1",`+
		`"normal":[{"kind":"mention","available":true,"gateway":null}],"important":[{"kind":"wait","wait_seconds":60},`+
		`{"kind":"call","gateway_id":"CNAAAAAAAAAAG1"}],"etag":"\"4\""}`, append(adminCSRF, "If-Match", `"4"`)...)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || f.calls[1] != "update 1" ||
		*f.versions[0] != 4 {
		t.Fatalf("update = %d %s %v", a.status, a.body, f.calls)
	}
	in := f.updates[0]
	if in.State != "custom" || *in.GatewayID != "CNAAAAAAAAAAG1" || len(*in.Rules.Normal) != 1 ||
		(*in.Rules.Important)[0].Kind != "wait" || *(*in.Rules.Important)[0].WaitSeconds != 60 ||
		*(*in.Rules.Important)[1].GatewayID != "CNAAAAAAAAAAG1" {
		t.Errorf("the update %+v", in)
	}
	if r := f.by[0]; r.Actor.PublicID != "SRAAAAAAAAAAAA" || r.Address.String() != "192.0.2.1" {
		t.Errorf("requester %+v", r)
	}
	a = x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"organization"}`,
		append(adminCSRF, "If-Match", "*")...)
	if a.status != http.StatusOK || f.versions[1] != nil || f.updates[1].Rules.Normal != nil {
		t.Errorf("If-Match * = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/notification-rules/reset", "", adminCSRF...)
	if a.status != http.StatusOK || f.calls[3] != "reset 1" || a.header.Get("ETag") != `"4"` {
		t.Errorf("reset = %d %s %v", a.status, a.body, f.calls)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"no_call"}`,
		adminCSRF...); a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d %s", a.status, a.body)
	}
	calls := len(f.calls)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/me/notification-rules", ""},
		{http.MethodPut, "/api/v1/me/notification-rules", `{"state":"no_call"}`},
		{http.MethodPost, "/api/v1/me/notification-rules/reset", ""},
	} {
		for _, token := range []string{fullToken, readOnlyToken, serviceToken} {
			a := x.call(t, c.method, c.path, c.body, "Authorization", "Bearer "+token, "If-Match", `"4"`)
			if a.status != http.StatusForbidden || a.code(t) != codeSessionRequired {
				t.Errorf("%s %s with %s = %d %s", c.method, c.path, token, a.status, a.body)
			}
		}
		if c.method == http.MethodGet {
			continue
		}
		if a := x.call(t, c.method, c.path, c.body, "Cookie", "admin-cookie", "If-Match", `"4"`); a.status !=
			http.StatusForbidden || a.code(t) != codeCSRFInvalid {
			t.Errorf("%s %s without the CSRF token = %d", c.method, c.path, a.status)
		}
	}
	if len(f.calls) != calls {
		t.Errorf("refused calls reached the rules: %v", f.calls[calls:])
	}
}

// TestNotificationRulesOfUser is C-31.FR-4 and C-31.AC-3: an Admin reads a User's rules with users:read and changes
// them with users:write, by session or token; a Responder and a Viewer, without users:write, get 403, and so does a
// token without it; a Responder still reads and changes their own rules under /me; an Admin's own rules change from
// the web session only.
func TestNotificationRulesOfUser(t *testing.T) {
	x, f := newNotificationRulesAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", "", "Authorization",
		"Bearer "+readOnlyToken)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || f.calls[0] != "get-of SRBBBBBBBBBBBB" {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	body := `{"state":"custom","normal":[{"kind":"mention"},{"kind":"mention"},{"kind":"mention"}],` +
		`"important":[{"kind":"mention"}]}`
	a = x.call(t, http.MethodPut, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", body, "Authorization",
		"Bearer "+fullToken, "If-Match", `"3"`)
	if a.status != http.StatusOK || f.calls[1] != "update-of SRBBBBBBBBBBBB" || *f.versions[0] != 3 ||
		len(*f.updates[0].Rules.Normal) != 3 || f.by[0].Actor.TokenID != 12 {
		t.Errorf("update = %d %s %v", a.status, a.body, f.calls)
	}
	calls := len(f.calls)
	for _, c := range []struct {
		name    string
		path    string
		headers []string
		code    string
	}{
		{"a Viewer", "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", []string{"Cookie", "viewer-cookie",
			"X-CSRF-Token", "csrf-viewer-cookie", "If-Match", `"3"`}, ""},
		{"a Viewer's read", "", []string{"Cookie", "viewer-cookie"}, ""},
		{"a Responder", "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", []string{"Cookie", "responder-cookie",
			"X-CSRF-Token", "csrf-responder-cookie", "If-Match", `"3"`}, ""},
		{"read-only", "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", []string{"Authorization",
			"Bearer " + readOnlyToken, "If-Match", `"3"`}, ""},
		{"own by token", "/api/v1/users/SRAAAAAAAAAAAA/notification-rules", []string{"Authorization",
			"Bearer " + fullToken, "If-Match", `"3"`}, codeSessionRequired},
	} {
		var a answer
		if c.path == "" {
			a = x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", "", c.headers...)
		} else {
			a = x.call(t, http.MethodPut, c.path, body, c.headers...)
		}
		if a.status != http.StatusForbidden || a.code(t) != c.code {
			t.Errorf("%s = %d %s", c.name, a.status, a.body)
		}
	}
	if len(f.calls) != calls {
		t.Errorf("refused calls reached the rules: %v", f.calls[calls:])
	}
	a = x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"no_call"}`, "Cookie", "responder-cookie",
		"X-CSRF-Token", "csrf-responder-cookie", "If-Match", `"3"`)
	if a.status != http.StatusOK || f.calls[len(f.calls)-1] != "update 5" {
		t.Errorf("a Responder's own rules = %d %s %v", a.status, a.body, f.calls)
	}
	a = x.call(t, http.MethodPut, "/api/v1/users/SRAAAAAAAAAAAA/notification-rules", body,
		append(adminCSRF, "If-Match", `"3"`)...)
	if a.status != http.StatusOK || f.calls[len(f.calls)-1] != "update-of SRAAAAAAAAAAAA" {
		t.Errorf("an Admin's own rules by session = %d %s", a.status, a.body)
	}
}

// TestNotificationRulesProblems maps the errors of internal/notificationrules: 422 with gateway_required,
// contact_required, required and out_of_range at their pointers, 412 for a stale If-Match, 404 for a deleted User and
// 500 for anything else.
func TestNotificationRulesProblems(t *testing.T) {
	x, f := newNotificationRulesAPI(t)
	for _, c := range []struct {
		err    error
		status int
		want   []string
	}{
		{&notificationrules.FieldError{Pointer: "/gateway_id", Code: "gateway_required", Detail: "x"}, 422,
			[]string{`"pointer":"/gateway_id"`, `"code":"gateway_required"`}},
		{&notificationrules.FieldError{Pointer: "/important/0", Code: "contact_required", Detail: "x"}, 422,
			[]string{`"pointer":"/important/0"`, `"code":"contact_required"`}},
		{&notificationrules.FieldError{Pointer: "/normal/1/wait_seconds", Code: "out_of_range", Detail: "x"}, 422,
			[]string{`"pointer":"/normal/1/wait_seconds"`, `"code":"out_of_range"`}},
		{notificationrules.ErrVersionMismatch, 412, []string{`"type":"` + problemBase + typePreconditionFailed}},
		{notificationrules.ErrUserNotFound, 404, []string{`"detail":"No such user."`}},
		{errors.New("db down"), 500, []string{`"type":"` + problemBase + typeInternal}},
	} {
		f.err = c.err
		answers := []answer{x.call(t, http.MethodPut, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules",
			`{"state":"call"}`, "Authorization", "Bearer "+fullToken, "If-Match", `"1"`)}
		if c.status != http.StatusNotFound { // the signed-in User always exists
			answers = append(answers, x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"call"}`,
				append(adminCSRF, "If-Match", `"1"`)...))
		}
		for _, a := range answers {
			if a.status != c.status {
				t.Errorf("%v = %d %s", c.err, a.status, a.body)
			}
			for _, w := range c.want {
				if !strings.Contains(string(a.body), w) {
					t.Errorf("%v: %s lacks %s", c.err, a.body, w)
				}
			}
		}
	}
	f.err = errors.New("db down")
	for _, a := range []answer{
		x.call(t, http.MethodGet, "/api/v1/me/notification-rules", "", "Cookie", "admin-cookie"),
		x.call(t, http.MethodPost, "/api/v1/me/notification-rules/reset", "", adminCSRF...),
		x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", "", "Authorization",
			"Bearer "+fullToken),
	} {
		if a.status != http.StatusInternalServerError {
			t.Errorf("a failure = %d %s", a.status, a.body)
		}
	}
	f.err = nil
	if a := x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"call"}`,
		append(adminCSRF, "If-Match", `W/"x"`)...); a.status != http.StatusPreconditionFailed {
		t.Errorf("an If-Match of no version = %d", a.status)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/users/SRBBBBBBBBBBBB/notification-rules", `{"state":"call"}`,
		"Authorization", "Bearer "+fullToken, "If-Match", `W/"x"`); a.status != http.StatusPreconditionFailed {
		t.Errorf("an If-Match of no version = %d", a.status)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/me/notification-rules", `{"state":"sometimes"}`,
		append(adminCSRF, "If-Match", `"1"`)...); a.status != http.StatusBadRequest {
		t.Errorf("an unknown state = %d %s", a.status, a.body)
	}
}
