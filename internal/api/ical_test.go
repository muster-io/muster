// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/schedules"
)

// icalToken is a well-formed iCal token of the fake, and disabledToken the token of a disabled User's feed.
const (
	icalToken     = "mstr_cal_abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"
	disabledToken = "mstr_cal_disabled"
	icalURL       = "https://muster.example.org/ical/" + icalToken + ".ics"
)

// fakeIcalFeeds stands for the iCal feeds of internal/schedules; err answers Calendar.
type fakeIcalFeeds struct {
	issued  *time.Time
	by      []schedules.Requester
	users   []schedules.FeedUser
	revoked int
	looked  []string
	err     error
	// issueErr answers Issue.
	issueErr error
	// revokeErr answers RevokeOfUser.
	revokeErr error
}

func (f *fakeIcalFeeds) Get(_ context.Context, userID int64) (schedules.Feed, error) {
	f.users = append(f.users, schedules.FeedUser{ID: userID})
	return schedules.Feed{IssuedAt: f.issued}, nil
}

func (f *fakeIcalFeeds) Issue(_ context.Context, r schedules.Requester, u schedules.FeedUser) (schedules.IssuedFeed,
	error) {
	f.by, f.users = append(f.by, r), append(f.users, u)
	if f.issueErr != nil {
		return schedules.IssuedFeed{}, f.issueErr
	}
	at := t0
	f.issued = &at
	return schedules.IssuedFeed{URL: icalURL, IssuedAt: t0}, nil
}

func (f *fakeIcalFeeds) Revoke(_ context.Context, r schedules.Requester, u schedules.FeedUser) error {
	f.by, f.users = append(f.by, r), append(f.users, u)
	f.revoked++
	f.issued = nil
	return nil
}

func (f *fakeIcalFeeds) RevokeOfUser(_ context.Context, r schedules.Requester, userPublicID string) error {
	f.by, f.users = append(f.by, r), append(f.users, schedules.FeedUser{PublicID: userPublicID})
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked++
	return nil
}

func (f *fakeIcalFeeds) Lookup(_ context.Context, value string) (schedules.FeedUser, error) {
	f.looked = append(f.looked, value)
	switch value {
	case icalToken:
		return schedules.FeedUser{ID: 2, PublicID: bobPublicID}, nil
	case disabledToken:
		return schedules.FeedUser{ID: 4, PublicID: "SRDDDDDDDDDDDD"}, schedules.ErrFeedNotFound
	}
	return schedules.FeedUser{}, schedules.ErrFeedNotFound
}

func (f *fakeIcalFeeds) Calendar(_ context.Context, u schedules.FeedUser) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte("BEGIN:VCALENDAR\r\nUID:" + u.PublicID + "\r\nEND:VCALENDAR\r\n"), nil
}

// TestIcalFeedOperations is C-27.FR-10 and C-27.AC-7 at the API: getMyIcalFeed says whether the caller has a feed
// and when it was issued, never its token, to the web session and to a Personal access token; issueMyIcalFeed gives
// the link once with 201 and revokeMyIcalFeed answers 204, both as the session's User with the Transport ui; a
// Personal access token gets 403 session_required on issuing and revoking, and a Service account token 403
// service_account_not_allowed on reading and, as on every operation that changes the caller's own account,
// session_required on issuing and revoking.
func TestIcalFeedOperations(t *testing.T) {
	x, _, _, _ := newTokensAPI(t)
	f := &fakeIcalFeeds{}
	x.srv.icalFeeds = f
	a := x.call(t, http.MethodGet, "/api/v1/me/ical-feed", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || string(bytes.TrimSpace(a.body)) != `{"exists":false,"issued_at":null}` {
		t.Fatalf("without a feed = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/ical-feed", "")
	var issued struct {
		URL      string    `json:"url"`
		IssuedAt time.Time `json:"issued_at"`
	}
	decodeInto(t, a, &issued)
	if a.status != http.StatusCreated || issued.URL != icalURL || !issued.IssuedAt.Equal(t0) {
		t.Fatalf("issue = %d %s", a.status, a.body)
	}
	by := f.by[0]
	if by.Actor.Kind != audit.ActorUser || by.Actor.ID != 1 || by.Transport != audit.TransportUI ||
		f.users[1] != (schedules.FeedUser{ID: 1, PublicID: "SRAAAAAAAAAAAA"}) {
		t.Errorf("issued by %+v for %+v", by, f.users[1])
	}
	for _, auth := range [][]string{{"Cookie", adminCookie}, bearer(fullToken)} {
		a = x.call(t, http.MethodGet, "/api/v1/me/ical-feed", "", auth...)
		if a.status != http.StatusOK || a.json(t)["exists"] != true ||
			a.json(t)["issued_at"] != t0.Format(time.RFC3339) || strings.Contains(string(a.body), "mstr_cal_") {
			t.Errorf("with a feed = %d %s", a.status, a.body)
		}
	}
	f.issueErr = schedules.ErrFeedUserInactive
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/ical-feed", ""); a.status != http.StatusUnauthorized {
		t.Errorf("issuing for a User who is no longer active = %d %s", a.status, a.body)
	}
	f.issueErr = nil
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/me/ical-feed", ""); a.status != http.StatusNoContent ||
		f.revoked != 1 {
		t.Errorf("revoke = %d %s", a.status, a.body)
	}
	calls := len(f.by)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, token := range []string{fullToken, serviceToken} {
			if a = x.call(t, method, "/api/v1/me/ical-feed", "", bearer(token)...); a.status != http.StatusForbidden ||
				a.code(t) != codeSessionRequired {
				t.Errorf("%s with %s = %d %s", method, token, a.status, a.body)
			}
		}
	}
	if a = x.call(t, http.MethodGet, "/api/v1/me/ical-feed", "", bearer(serviceToken)...); a.status !=
		http.StatusForbidden || a.code(t) != codeServiceAccountDenied {
		t.Errorf("a Service account reads = %d %s", a.status, a.body)
	}
	if len(f.by) != calls {
		t.Errorf("a refused request reached the feeds: %+v", f.by[calls:])
	}
	if a = x.call(t, http.MethodPost, "/api/v1/me/ical-feed", "", "Cookie", adminCookie); a.status !=
		http.StatusForbidden || a.code(t) != codeCSRFInvalid {
		t.Errorf("without the CSRF token = %d %s", a.status, a.body)
	}
	// The feed is outside /api/v1: under it, every answer is a problem without an instance, which would repeat the
	// token — 404 for the path of the feed and the paths around it, 405 for another method.
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/v1/ical/" + icalToken + ".ics", http.StatusNotFound},
		{http.MethodHead, "/api/v1/ical/" + icalToken + ".ics", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/ical/" + icalToken, http.StatusNotFound},
		{http.MethodGet, "/api/v1/ical/" + icalToken + ".ics/x", http.StatusNotFound},
		{http.MethodGet, "/api/v1//ical/" + icalToken + ".ics", http.StatusNotFound},
		{http.MethodGet, "/api/v1/./ical/" + icalToken + ".ics", http.StatusNotFound},
		{http.MethodGet, "/api/v1/nothing/" + icalToken, http.StatusNotFound},
		{http.MethodPost, "/api/v1/ical/" + icalToken + ".ics", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/v1/ical/" + icalToken + ".ics", http.StatusMethodNotAllowed},
	} {
		a = x.call(t, c.method, c.path, "", "Cookie", adminCookie)
		if a.status != c.status || a.header.Get("Content-Type") != contentTypeProblem ||
			strings.Contains(string(a.body), icalToken) || strings.Contains(string(a.body), `"instance"`) ||
			(c.status == http.StatusMethodNotAllowed && a.header.Get("Allow") != "GET") {
			t.Errorf("%s %s = %d %v %s", c.method, c.path, a.status, a.header, a.body)
		}
	}
	if a = x.call(t, http.MethodGet, "/api/v1/nothing", ""); a.json(t)["instance"] != "/api/v1/nothing" {
		t.Errorf("another path loses its instance: %s", a.body)
	}
}

// TestIcalHandler is C-27.FR-10 and C-27.AC-7: GET /ical/{token}.ics answers the calendar as text/calendar without
// logging, HEAD too; an unknown token, a disabled User's and a path that is not a feed answer 404 with a problem that
// carries no instance and nothing of the token, its User or the Organization, logged as ical_feed_not_found with the
// masked path and the User where known; a failure answers 500, logged as ical_feed_failed; another method gets 405.
// No answer and no log line carries a token.
func TestIcalHandler(t *testing.T) {
	var log bytes.Buffer
	f := &fakeIcalFeeds{}
	h := IcalHandler{Feeds: f, Log: logging.New(&log, logging.LevelInfo)}
	serve := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, path, nil))
		return w
	}
	lines := func() []map[string]any {
		var out []map[string]any
		for l := range strings.Lines(log.String()) {
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("line %q: %v", l, err)
			}
			out = append(out, m)
		}
		log.Reset()
		return out
	}

	w := serve(http.MethodGet, "/ical/"+icalToken+".ics")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/calendar; charset=utf-8" ||
		w.Body.String() != "BEGIN:VCALENDAR\r\nUID:"+bobPublicID+"\r\nEND:VCALENDAR\r\n" ||
		w.Header().Get("Cache-Control") != "no-store" || len(lines()) != 0 {
		t.Fatalf("a feed = %d %v %q", w.Code, w.Header(), w.Body)
	}
	if w = serve(http.MethodHead, "/ical/"+icalToken+".ics"); w.Code != http.StatusOK {
		t.Errorf("HEAD = %d", w.Code)
	}
	lines()

	for path, user := range map[string]any{
		"/ical/mstr_cal_" + strings.Repeat("z", 52) + ".ics": nil,
		"/ical/" + disabledToken + ".ics":                    "SRDDDDDDDDDDDD",
		"/ical/garbage.ics":                                  nil,
		"/ical/" + icalToken:                                 nil,
		"/ical/":                                             nil,
	} {
		looked := len(f.looked)
		w = serve(http.MethodGet, path)
		var p map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != contentTypeProblem ||
			p["type"] != problemBase+typeNotFound || p["status"] != float64(404) || len(p) != 4 ||
			strings.Contains(w.Body.String(), "mstr_cal_") || strings.Contains(w.Body.String(), "SR") {
			t.Errorf("%s = %d %s", path, w.Code, w.Body)
		}
		l := lines()
		if len(l) != 1 || l[0]["event"] != "ical_feed_not_found" || l[0]["level"] != "INFO" ||
			l[0]["path"] != "/ical/[redacted].ics" || l[0]["user"] != user {
			t.Errorf("%s logged %v", path, l)
		}
		if !strings.HasSuffix(path, ".ics") && len(f.looked) != looked {
			t.Errorf("%s was looked up", path)
		}
	}

	f.err = errors.New("the database is unavailable")
	w = serve(http.MethodGet, "/ical/"+icalToken+".ics")
	l := lines()
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "database") || len(l) != 1 ||
		l[0]["event"] != "ical_feed_failed" || l[0]["level"] != "ERROR" || l[0]["path"] != IcalMaskedPath ||
		l[0]["user"] != bobPublicID || l[0]["error"] != "the database is unavailable" {
		t.Errorf("a failure = %d %s, logged %v", w.Code, w.Body, l)
	}

	w = serve(http.MethodPost, "/ical/"+icalToken+".ics")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" || len(lines()) != 0 {
		t.Errorf("POST = %d %v", w.Code, w.Header())
	}
	for _, l := range f.looked {
		if strings.Contains(log.String(), l) {
			t.Errorf("a log line carries %q", l)
		}
	}
}

// TestRevokeUserIcalFeed is the Admin's revoke of a User's iCal feed: revokeUserIcalFeed answers 204 with users:write,
// by session or by token, as the actor with its Transport; a Viewer, a token without users:write and a missing CSRF
// token get 403 and never reach the feeds; an unknown User is 404.
func TestRevokeUserIcalFeed(t *testing.T) {
	x, _, _, _ := newTokensAPI(t)
	f := &fakeIcalFeeds{}
	x.srv.icalFeeds = f
	const path = "/api/v1/users/SRBBBBBBBBBBBB/ical-feed"
	if a := x.mutate(t, adminCookie, http.MethodDelete, path, ""); a.status != http.StatusNoContent || f.revoked != 1 ||
		f.users[0].PublicID != "SRBBBBBBBBBBBB" || f.by[0].Transport != audit.TransportUI {
		t.Fatalf("by the session = %d %s %+v", a.status, a.body, f.by)
	}
	if a := x.call(t, http.MethodDelete, path, "", bearer(fullToken)...); a.status != http.StatusNoContent ||
		f.revoked != 2 || f.by[1].Transport != audit.TransportAPI || f.by[1].Actor.TokenName != "full" {
		t.Errorf("by a token = %d %s %+v", a.status, a.body, f.by[1])
	}
	calls := len(f.by)
	if a := x.mutate(t, viewerCookie, http.MethodDelete, path, ""); a.status != http.StatusForbidden {
		t.Errorf("a Viewer = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodDelete, path, "", bearer(readOnlyToken)...); a.status != http.StatusForbidden {
		t.Errorf("a token without users:write = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodDelete, path, "", "Cookie", adminCookie); a.status != http.StatusForbidden ||
		a.code(t) != codeCSRFInvalid {
		t.Errorf("without the CSRF token = %d %s", a.status, a.body)
	}
	if len(f.by) != calls {
		t.Errorf("a refused request reached the feeds: %+v", f.by[calls:])
	}
	f.revokeErr = schedules.ErrFeedUserNotFound
	if a := x.mutate(t, adminCookie, http.MethodDelete, path, ""); a.status != http.StatusNotFound {
		t.Errorf("an unknown User = %d %s", a.status, a.body)
	}
}

// TestIcalFeedMetric is the counter of the fetches of the feeds: served for a calendar, not_found for a token that
// names no feed, failed for a failure, and nothing for a refused method; it has no label for the token or the User.
func TestIcalFeedMetric(t *testing.T) {
	f := &fakeIcalFeeds{}
	h := NewIcalHandler(f, logging.New(&bytes.Buffer{}, logging.LevelInfo))
	count := func(outcome string) uint64 { return metrics.IcalFeedRequests.With(outcome).Get() }
	before := map[string]uint64{}
	for _, o := range metrics.IcalFeedOutcomes {
		before[o] = count(o)
	}
	serve := func(method, path string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), method, path, nil))
	}
	serve(http.MethodGet, "/ical/"+icalToken+".ics")
	serve(http.MethodHead, "/ical/"+icalToken+".ics")
	serve(http.MethodGet, "/ical/"+disabledToken+".ics")
	serve(http.MethodGet, "/ical/garbage.ics")
	serve(http.MethodPost, "/ical/"+icalToken+".ics")
	f.err = errors.New("the database is unavailable")
	serve(http.MethodGet, "/ical/"+icalToken+".ics")
	for outcome, want := range map[string]uint64{"served": 2, "not_found": 2, "failed": 1} {
		if got := count(outcome) - before[outcome]; got != want {
			t.Errorf("%s = %d, want %d", outcome, got, want)
		}
	}
}
