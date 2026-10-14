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
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
)

// fakeAccountLinks stands for internal/accountlinks: it records who asked for what and answers err when set.
type fakeAccountLinks struct {
	links   []accountlinks.Link
	by      []accountlinks.Requester
	calls   []string
	err     error
	confirm string
}

func (f *fakeAccountLinks) record(r accountlinks.Requester, call string) error {
	f.by, f.calls = append(f.by, r), append(f.calls, call)
	return f.err
}

func (f *fakeAccountLinks) List(_ context.Context, userID int64) ([]accountlinks.Link, error) {
	f.calls = append(f.calls, "list "+strconv.FormatInt(userID, 10))
	return f.links, f.err
}

func (f *fakeAccountLinks) ListOfUser(_ context.Context, userPublicID string) ([]accountlinks.Link, error) {
	f.calls = append(f.calls, "list-of "+userPublicID)
	return f.links, f.err
}

func (f *fakeAccountLinks) Connections(context.Context) ([]accountlinks.LinkableConnection, error) {
	f.calls = append(f.calls, "connections")
	return []accountlinks.LinkableConnection{{PublicID: "CNAAAAAAAAAAA2", Name: "mm", Messenger: "mattermost"},
		{PublicID: "CNAAAAAAAAAAA1", Name: "Dev Telegram", Messenger: "telegram"}}, f.err
}

func (f *fakeAccountLinks) Unlink(_ context.Context, r accountlinks.Requester, id string) error {
	return f.record(r, "unlink "+id)
}

func (f *fakeAccountLinks) UnlinkOfUser(_ context.Context, r accountlinks.Requester, user, id string) error {
	return f.record(r, "unlink-of "+user+" "+id)
}

func (f *fakeAccountLinks) StartTelegram(_ context.Context, r accountlinks.Requester, conn string) (
	accountlinks.TelegramStart, error) {
	return accountlinks.TelegramStart{DeepLink: "https://t.me/muster_dev_bot?start=" + strings.Repeat("A", 43),
		ExpiresAt: t0.Add(10 * time.Minute)}, f.record(r, "telegram "+conn)
}

func (f *fakeAccountLinks) StartMattermost(_ context.Context, r accountlinks.Requester, conn, username string) (
	accountlinks.MattermostStarted, error) {
	return accountlinks.MattermostStarted{RequestPublicID: "ARAAAAAAAAAAA1", ExpiresAt: t0.Add(10 * time.Minute),
		AttemptsRemaining: 5}, f.record(r, "mattermost "+conn+" "+username)
}

func (f *fakeAccountLinks) ConfirmMattermost(_ context.Context, r accountlinks.Requester, id, code string) (
	accountlinks.Link, error) {
	f.confirm = code
	return f.links[0], f.record(r, "confirm "+id)
}

func newAccountLinksAPI(t *testing.T) (*testAPI, *fakeAccountLinks) {
	t.Helper()
	x := newTestAPI(t)
	x.srv.tokens = newFakeTokens(clock.Real{}, x)
	used := t0.Add(time.Hour)
	f := &fakeAccountLinks{links: []accountlinks.Link{
		{PublicID: "AKAAAAAAAAAAA1", Messenger: "telegram", ConnectionPublicID: "CNAAAAAAAAAAA1",
			ConnectionName: "Dev Telegram", ExternalID: "7001", Username: ptr("alice_t"), LastUsedAt: &used,
			CreatedAt: t0},
		{PublicID: "AKAAAAAAAAAAA2", Messenger: "mattermost", ConnectionPublicID: "CNAAAAAAAAAAA2",
			ConnectionName: "mm", ExternalID: "u-bob", CreatedAt: t0},
	}}
	x.srv.accountLinks = f
	return x, f
}

var adminCSRF = []string{"Cookie", "admin-cookie", "X-CSRF-Token", "csrf-admin-cookie"}

// TestAccountLinksLists is C-18.FR-6 and FR-10: the caller's links and a User's links with their messenger,
// Connection, username and last use; the Connections to link through for any signed-in User with only their id, name
// and messenger; a Service account gets 403 service_account_not_allowed under /me.
func TestAccountLinksLists(t *testing.T) {
	x, f := newAccountLinksAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me/account-links", "", "Cookie", "viewer-cookie")
	items, _ := a.json(t)["items"].([]any)
	if a.status != http.StatusOK || len(items) != 2 || f.calls[0] != "list 2" {
		t.Fatalf("= %d %s %v", a.status, a.body, f.calls)
	}
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	conn := first["connection"].(map[string]any)
	if first["username"] != "alice_t" || first["last_used_at"] != "2026-10-05T13:00:00Z" ||
		conn["id"] != "CNAAAAAAAAAAA1" || conn["name"] != "Dev Telegram" || second["username"] != nil ||
		second["last_used_at"] != nil || second["messenger"] != "mattermost" {
		t.Errorf("items %v", items)
	}
	a = x.call(t, http.MethodGet, "/api/v1/me/account-links/connections", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusOK || !strings.Contains(string(a.body),
		`{"id":"CNAAAAAAAAAAA2","messenger":"mattermost","name":"mm"}`) {
		t.Errorf("connections = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/me/account-links", "", "Authorization", "Bearer "+readOnlyToken)
	if a.status != http.StatusOK {
		t.Errorf("a Personal access token = %d %s", a.status, a.body)
	}
	for _, path := range []string{"/api/v1/me/account-links", "/api/v1/me/account-links/connections"} {
		a = x.call(t, http.MethodGet, path, "", "Authorization", "Bearer "+serviceToken)
		if a.status != http.StatusForbidden || a.code(t) != codeServiceAccountDenied {
			t.Errorf("a Service account on %s = %d %s", path, a.status, a.body)
		}
	}
	a = x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/account-links", "", "Authorization",
		"Bearer "+readOnlyToken)
	if a.status != http.StatusOK || f.calls[len(f.calls)-1] != "list-of SRBBBBBBBBBBBB" {
		t.Errorf("an Admin's list = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/account-links", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusForbidden {
		t.Errorf("a Viewer's list of a User = %d", a.status)
	}
	f.err = accountlinks.ErrUserNotFound
	a = x.call(t, http.MethodGet, "/api/v1/users/SRBBBBBBBBBBBB/account-links", "", adminCSRF...)
	if a.status != http.StatusNotFound {
		t.Errorf("an unknown User = %d", a.status)
	}
	f.err = errors.New("database down")
	for _, path := range []string{"/api/v1/me/account-links", "/api/v1/me/account-links/connections"} {
		if a := x.call(t, http.MethodGet, path, "", adminCSRF...); a.status != http.StatusInternalServerError {
			t.Errorf("a failed %s = %d", path, a.status)
		}
	}
}

// TestAccountLinksStart is C-18.FR-1 to FR-3, C-18.AC-4 and C-03.AC-14: starting, confirming and removing one's own
// link accept the web session with its CSRF token only — a token, an Admin's included, gets 403 session_required — and
// act on the signed-in User; the answers carry the deep link, the started request and the link.
func TestAccountLinksStart(t *testing.T) {
	x, f := newAccountLinksAPI(t)
	a := x.call(t, http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"CNAAAAAAAAAAA1"}`,
		adminCSRF...)
	if a.status != http.StatusCreated || a.json(t)["deep_link"] != "https://t.me/muster_dev_bot?start="+
		strings.Repeat("A", 43) || a.json(t)["expires_at"] != "2026-10-05T12:10:00Z" ||
		f.calls[0] != "telegram CNAAAAAAAAAAA1" {
		t.Fatalf("= %d %s %v", a.status, a.body, f.calls)
	}
	if r := f.by[0]; r.UserID != 1 || r.UserPublicID != "SRAAAAAAAAAAAA" || r.Transport != audit.TransportUI ||
		r.Actor.Kind != audit.ActorUser || r.Address.String() != "192.0.2.1" {
		t.Errorf("requester %+v", r)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/account-links/mattermost",
		`{"connection_id":"CNAAAAAAAAAAA2","username":"@bob"}`, adminCSRF...)
	if a.status != http.StatusAccepted || a.json(t)["request_id"] != "ARAAAAAAAAAAA1" ||
		a.json(t)["attempts_remaining"] != float64(5) || f.calls[1] != "mattermost CNAAAAAAAAAAA2 @bob" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation",
		`{"request_id":"ARAAAAAAAAAAA1","code":"ABCD2345"}`, adminCSRF...)
	if a.status != http.StatusCreated || a.json(t)["id"] != "AKAAAAAAAAAAA1" || f.confirm != "ABCD2345" ||
		f.calls[2] != "confirm ARAAAAAAAAAAA1" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodDelete, "/api/v1/me/account-links/AKAAAAAAAAAAA1", "", adminCSRF...)
	if a.status != http.StatusNoContent || f.calls[3] != "unlink AKAAAAAAAAAAA1" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	calls := len(f.calls)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"CNAAAAAAAAAAA1"}`},
		{http.MethodPost, "/api/v1/me/account-links/mattermost", `{"connection_id":"CNAAAAAAAAAAA2","username":"x"}`},
		{http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation",
			`{"request_id":"ARAAAAAAAAAAA1","code":"x"}`},
		{http.MethodDelete, "/api/v1/me/account-links/AKAAAAAAAAAAA1", ""},
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
		t.Errorf("a refused request reached the Account links: %v", f.calls[calls:])
	}
}

// TestAccountLinksAdmin is C-18.FR-6, AC-4 and C-03.AC-14: an Admin removes a User's link with users:write, by session
// or token, as the actor, but their own link from the web session only — a token gets 403 session_required; a POST to
// a User's links is 405 with Allow: GET.
func TestAccountLinksAdmin(t *testing.T) {
	x, f := newAccountLinksAPI(t)
	a := x.call(t, http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/account-links/AKAAAAAAAAAAA2", "", "Authorization",
		"Bearer "+fullToken)
	if a.status != http.StatusNoContent || f.calls[0] != "unlink-of SRBBBBBBBBBBBB AKAAAAAAAAAAA2" ||
		f.by[0].Transport != audit.TransportAPI || f.by[0].Actor.TokenName != "full" {
		t.Fatalf("= %d %s %+v", a.status, a.body, f.by)
	}
	if a := x.call(t, http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/account-links/AKAAAAAAAAAAA2", "",
		"Authorization", "Bearer "+serviceToken); a.status != http.StatusNoContent ||
		f.by[1].Actor.Kind != audit.ActorServiceAccount {
		t.Errorf("a Service account token = %d %+v", a.status, f.by[1])
	}
	if a := x.call(t, http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/account-links/AKAAAAAAAAAAA2", "",
		"Authorization", "Bearer "+readOnlyToken); a.status != http.StatusForbidden {
		t.Errorf("without users:write = %d", a.status)
	}
	calls := len(f.calls)
	for _, user := range []string{"SRAAAAAAAAAAAA", "sraaaaaaaaaaaa"} {
		if a := x.call(t, http.MethodDelete, "/api/v1/users/"+user+"/account-links/AKAAAAAAAAAAA1", "",
			"Authorization", "Bearer "+fullToken); a.status != http.StatusForbidden ||
			a.code(t) != codeSessionRequired || len(f.calls) != calls {
			t.Errorf("an Admin's own link by token = %d %s", a.status, a.body)
		}
	}
	if a := x.call(t, http.MethodDelete, "/api/v1/users/SRAAAAAAAAAAAA/account-links/AKAAAAAAAAAAA1", "",
		adminCSRF...); a.status != http.StatusNoContent ||
		f.calls[len(f.calls)-1] != "unlink-of SRAAAAAAAAAAAA AKAAAAAAAAAAA1" {
		t.Errorf("an Admin's own link from the web session = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPost, "/api/v1/users/SRBBBBBBBBBBBB/account-links", "{}", "Authorization",
		"Bearer "+fullToken)
	if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "GET" ||
		a.json(t)["type"] != problemBase+"method-not-allowed" {
		t.Errorf("POST a User's links = %d %s", a.status, a.body)
	}
	f.err = accountlinks.ErrNotFound
	if a := x.call(t, http.MethodDelete, "/api/v1/users/SRBBBBBBBBBBBB/account-links/AKAAAAAAAAAAA9", "",
		adminCSRF...); a.status != http.StatusNotFound {
		t.Errorf("an unknown link = %d", a.status)
	}
}

// TestAccountLinksProblems: the refusals of the Account links as problems — 422 at the field, with the attempts left
// for link_code_invalid, 409 account_linked_elsewhere, 429 link_code_requests_exceeded with Retry-After, 409 for a
// refusal of the messenger, 503 with Retry-After without a limiter token, 404 for an unknown link or request.
func TestAccountLinksProblems(t *testing.T) {
	x, f := newAccountLinksAPI(t)
	start := func() answer {
		return x.call(t, http.MethodPost, "/api/v1/me/account-links/mattermost",
			`{"connection_id":"CNAAAAAAAAAAA2","username":"bob"}`, adminCSRF...)
	}
	f.err = &accountlinks.FieldError{Pointer: "/username", Code: "messenger_user_not_found", Detail: "No such user."}
	a := start()
	errs, _ := a.json(t)["errors"].([]any)
	if a.status != http.StatusUnprocessableEntity || len(errs) != 1 ||
		errs[0].(map[string]any)["code"] != "messenger_user_not_found" ||
		errs[0].(map[string]any)["pointer"] != "/username" || strings.Contains(string(a.body), "attempts_remaining") {
		t.Errorf("a field error = %d %s", a.status, a.body)
	}
	left := 3
	f.err = &accountlinks.FieldError{Pointer: "/code", Code: "link_code_invalid", Detail: "Wrong.",
		AttemptsRemaining: &left}
	a = x.call(t, http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation",
		`{"request_id":"ARAAAAAAAAAAA9","code":"x"}`, adminCSRF...)
	errs, _ = a.json(t)["errors"].([]any)
	if a.status != http.StatusUnprocessableEntity || len(errs) != 1 ||
		errs[0].(map[string]any)["code"] != "link_code_invalid" || a.json(t)["attempts_remaining"] != float64(3) {
		t.Errorf("a wrong code = %d %s", a.status, a.body)
	}
	f.err = accountlinks.ErrLinkedElsewhere
	if a := start(); a.status != http.StatusConflict || a.code(t) != codeAccountLinkedElsewhere {
		t.Errorf("a conflict = %d %s", a.status, a.body)
	}
	f.err = &accountlinks.RateLimitedError{RetryAfter: time.Hour}
	if a := start(); a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "3600" ||
		a.json(t)["retry_after_seconds"] != float64(3600) || a.json(t)["type"] != problemBase+"rate-limited" ||
		a.code(t) != codeLinkCodeRequestsExceeded {
		t.Errorf("a limit = %d %s", a.status, a.body)
	}
	f.err = &accountlinks.MessengerError{Call: "the direct message", Outcome: "fatal", Status: 403}
	if a := start(); a.status != http.StatusConflict || a.code(t) != "" || strings.Contains(string(a.body), "403") {
		t.Errorf("a refusal of the messenger = %d %s", a.status, a.body)
	}
	f.err = &delivery.LimitedError{RetryAfter: 2 * time.Second}
	if a := start(); a.status != http.StatusServiceUnavailable || a.header.Get("Retry-After") != "2" {
		t.Errorf("no limiter token = %d %s", a.status, a.body)
	}
	f.err = &accountlinks.FieldError{Pointer: "/connection_id", Code: "unknown_id", Detail: "No Telegram Connection."}
	if a := x.call(t, http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"CNAAAAAAAAAAA2"}`,
		adminCSRF...); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a Mattermost Connection for Telegram = %d %s", a.status, a.body)
	}
	f.err = accountlinks.ErrNotFound
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"ARAAAAAAAAAAA9","code":"x"}`},
		{http.MethodDelete, "/api/v1/me/account-links/AKAAAAAAAAAAA9", ""},
	} {
		if a := x.call(t, c.method, c.path, c.body, adminCSRF...); a.status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	f.err = nil
	for _, path := range []string{"/api/v1/me/account-links/telegram", "/api/v1/me/account-links/mattermost",
		"/api/v1/me/account-links/mattermost/confirmation"} {
		if a := x.call(t, http.MethodPost, path, `{}`, adminCSRF...); a.status != http.StatusBadRequest {
			t.Errorf("an empty body to %s = %d", path, a.status)
		}
	}
}

// TestAccountLinksRequests: the handlers refuse a request without an identity or a body before reaching the Account
// links, and leave an error they do not know to the generic problem.
func TestAccountLinksRequests(t *testing.T) {
	x, f := newAccountLinksAPI(t)
	ctx := t.Context()
	if _, err := x.srv.StartTelegramLink(ctx, gen.StartTelegramLinkRequestObject{}); !errors.Is(err,
		errUnauthenticated) {
		t.Errorf("no identity = %v", err)
	}
	for name, call := range map[string]func(context.Context) error{
		"list": func(ctx context.Context) error {
			_, err := x.srv.ListMyAccountLinks(ctx, gen.ListMyAccountLinksRequestObject{})
			return err
		},
		"delete": func(ctx context.Context) error {
			_, err := x.srv.DeleteMyAccountLink(ctx, gen.DeleteMyAccountLinkRequestObject{})
			return err
		},
		"mattermost": func(ctx context.Context) error {
			_, err := x.srv.StartMattermostLink(ctx, gen.StartMattermostLinkRequestObject{})
			return err
		},
		"confirm": func(ctx context.Context) error {
			_, err := x.srv.ConfirmMattermostLink(ctx, gen.ConfirmMattermostLinkRequestObject{})
			return err
		},
		"admin": func(ctx context.Context) error {
			_, err := x.srv.DeleteUserAccountLink(ctx, gen.DeleteUserAccountLinkRequestObject{})
			return err
		},
	} {
		if err := call(ctx); !errors.Is(err, errUnauthenticated) {
			t.Errorf("%s without an identity = %v", name, err)
		}
	}
	id := auth.WithIdentity(ctx, &auth.Identity{Session: x.sessions.sessions["admin-cookie"],
		Transport: audit.TransportUI})
	for name, call := range map[string]func() error{
		"telegram": func() error {
			_, err := x.srv.StartTelegramLink(id, gen.StartTelegramLinkRequestObject{})
			return err
		},
		"mattermost": func() error {
			_, err := x.srv.StartMattermostLink(id, gen.StartMattermostLinkRequestObject{})
			return err
		},
		"confirm": func() error {
			_, err := x.srv.ConfirmMattermostLink(id, gen.ConfirmMattermostLinkRequestObject{})
			return err
		},
	} {
		if p, ok := errors.AsType[*Problem](call()); !ok || p.Status != http.StatusBadRequest {
			t.Errorf("%s without a body = %v", name, p)
		}
	}
	if _, err := x.srv.ConfirmMattermostLink(id, gen.ConfirmMattermostLinkRequestObject{
		Body: &gen.MattermostLinkConfirm{RequestId: "ARAAAAAAAAAAA1"}}); err != nil || f.confirm != "" {
		t.Errorf("a confirmation without a code = %v", err)
	}
	if err := linkProblem(errors.New("other")); err.Error() != "other" {
		t.Errorf("an unknown error = %v", err)
	}
}
