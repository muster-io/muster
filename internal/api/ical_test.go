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
}

func (f *fakeIcalFeeds) Get(_ context.Context, userID int64) (schedules.Feed, error) {
	f.users = append(f.users, schedules.FeedUser{ID: userID})
	return schedules.Feed{IssuedAt: f.issued}, nil
}

func (f *fakeIcalFeeds) Issue(_ context.Context, r schedules.Requester, u schedules.FeedUser) (schedules.IssuedFeed,
	error) {
	f.by, f.users = append(f.by, r), append(f.users, u)
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
