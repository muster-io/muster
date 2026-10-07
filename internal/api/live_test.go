// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/organization"
)

// fakeNotices stands for the notice watcher: the recovery notice for everyone and the Admins' notice that no replica
// leads.
type fakeNotices struct {
	admin []bool
	err   error
}

func (f *fakeNotices) Visible(_ context.Context, admin bool) ([]organization.Notice, error) {
	f.admin = append(f.admin, admin)
	notices := []organization.Notice{{Kind: organization.NoticeRecoveringAfterDowntime,
		Audience: organization.AudienceAll, Until: t0.Add(15 * time.Minute)}}
	if admin {
		notices = append(notices, organization.Notice{Kind: organization.NoticeNoReplicaLeading,
			Audience: organization.AudienceAdmins, Since: t0})
	}
	return notices, f.err
}

// TestListSystemNotices is C-03.FR-18 and C-02.FR-24: the recovery notice reaches every signed-in identity, the
// notice that no replica leads only one that holds system-status:read.
func TestListSystemNotices(t *testing.T) {
	x := newTestAPI(t)
	f := &fakeNotices{}
	x.srv.notices = f
	a := x.call(t, http.MethodGet, "/api/v1/system-notices", "", "Cookie", viewerCookie)
	if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) !=
		`{"items":[{"audience":"all","kind":"recovering_after_downtime","since":null,"until":"2026-10-05T12:15:00Z"}]}` {
		t.Errorf("a Viewer = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/system-notices", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || len(a.json(t)["items"].([]any)) != 2 ||
		!strings.Contains(string(a.body), `"kind":"no_replica_leading","since":"2026-10-05T12:00:00Z","until":null`) {
		t.Errorf("an Admin = %d %s", a.status, a.body)
	}
	if len(f.admin) != 2 || f.admin[0] || !f.admin[1] {
		t.Errorf("asked for Admins %v", f.admin)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/system-notices", ""); a.status != http.StatusUnauthorized {
		t.Errorf("without a session = %d", a.status)
	}
	f.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, "/api/v1/system-notices", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
	if _, err := x.srv.ListSystemNotices(t.Context(), gen.ListSystemNoticesRequestObject{}); !errors.Is(err,
		errUnauthenticated) {
		t.Errorf("without an identity: %v", err)
	}
}

// liveSessions are the sessions the Hub of the tests keeps open.
type liveSessions map[int64]bool

func (l liveSessions) check(_ context.Context, ids []int64) ([]int64, error) {
	var out []int64
	for _, id := range ids {
		if l[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// TestStreamLiveUpdates is C-09.FR-25 and C-09.AC-24: the stream answers text/event-stream, starts with retry: 3000
// and delivers hints as events named hint; a hint for Admins reaches only Admins; the stream ends when its session
// ends and the reconnect gets 401; a limited session gets 403 and a session past its stream limit 429.
func TestStreamLiveUpdates(t *testing.T) {
	x := newTestAPI(t)
	sessions := liveSessions{1: true, 2: true}
	hub := live.NewHub(1, sessions.check)
	x.srv.live = hub
	srv := httptest.NewServer(x.srv)
	defer srv.Close()

	open := func(cookie string) (*http.Response, *bufio.Reader) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/live-updates", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie}) //nolint:gosec // G124: a request cookie
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	line := func(r *bufio.Reader) string {
		t.Helper()
		s, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read the stream: %v", err)
		}
		return strings.TrimSuffix(s, "\n")
	}
	admin, ar := open(adminCookie)
	defer admin.Body.Close()
	viewer, vr := open(viewerCookie)
	defer viewer.Body.Close()
	for _, resp := range []*http.Response{admin, viewer} {
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
			resp.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("= %d %v", resp.StatusCode, resp.Header)
		}
	}
	for _, r := range []*bufio.Reader{ar, vr} {
		if l := line(r); l != "retry: 3000" {
			t.Errorf("first line %q", l)
		}
		line(r)
	}
	for hub.Streams() != 2 {
		time.Sleep(time.Millisecond)
	}
	hub.Send(live.Hint{Type: live.HintSystemNotices}, func(s live.Subscriber) bool { return s.Admin })
	hub.Receive(db.Hint{OrgID: 1, Type: live.HintOrganization})
	// Both Roles read Alert Groups, so both get their hints.
	hub.Receive(db.Hint{OrgID: 1, Type: live.HintAlertGroup, ID: "AGAAAAAAAAAAAA"})
	want := []string{"event: hint", "id: 1", `data: {"type":"system-notices","id":null}`, "", "event: hint", "id: 2",
		`data: {"type":"organization","id":null}`, "", "event: hint", "id: 3",
		`data: {"type":"alert-group","id":"AGAAAAAAAAAAAA"}`, ""}
	for _, w := range want {
		if l := line(ar); l != w {
			t.Errorf("Admin's stream: %q, want %q", l, w)
		}
	}
	for _, w := range []string{"event: hint", "id: 1", `data: {"type":"organization","id":null}`, "",
		"event: hint", "id: 2", `data: {"type":"alert-group","id":"AGAAAAAAAAAAAA"}`} {
		if l := line(vr); l != w {
			t.Errorf("Viewer's stream: %q, want %q", l, w)
		}
	}

	// The viewer's session ends: the next check closes its stream, and the reconnect gets 401.
	delete(sessions, 2)
	delete(x.sessions.sessions, viewerCookie)
	hub.CheckSessions(t.Context())
	line(vr)
	if _, err := vr.ReadString('\n'); err == nil {
		t.Error("the stream of an ended session is still open")
	}
	resp, _ := open(viewerCookie)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the reconnect = %d", resp.StatusCode)
	}
	resp, _ = open("limited-cookie")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a limited session = %d", resp.StatusCode)
	}
	for range live.MaxStreamsPerSession - 1 {
		if _, err := hub.Subscribe(live.Subscriber{SessionID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	resp, _ = open(adminCookie)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "5" {
		t.Errorf("a stream beyond the limit = %d %v", resp.StatusCode, resp.Header)
	}
	hub.Close()
	if _, err := ar.ReadString('\n'); err == nil {
		t.Error("the Hub closed and the stream is still open")
	}
	if _, err := x.srv.StreamLiveUpdates(t.Context(), gen.StreamLiveUpdatesRequestObject{}); !errors.Is(err,
		errUnauthenticated) {
		t.Errorf("without an identity: %v", err)
	}
}
