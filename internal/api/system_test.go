// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/systemstatus"
)

// fakeSystemStatus answers the System status it holds; err answers every read.
type fakeSystemStatus struct {
	status systemstatus.Status
	err    error
}

func (f *fakeSystemStatus) Get(context.Context) (systemstatus.Status, error) {
	return f.status, f.err
}

const responderCookie = "responder-cookie"

// newSystemStatusAPI adds the System status and a Responder's session.
func newSystemStatusAPI(t *testing.T) (*testAPI, *fakeSystemStatus) {
	t.Helper()
	x := newTestAPI(t)
	responder := user(7, "SREEEEEEEEEEEE", "responder", auth.RoleResponder)
	x.users.users[7] = responder
	x.sessions.sessions[responderCookie] = session(7, responder, auth.StateActive)
	x.sessions.cookies[7] = responderCookie
	f := &fakeSystemStatus{}
	x.srv.systemStatus = f
	return x, f
}

// TestGetSystemStatus covers C-19.FR-10 and C-19.AC-6 at the API: an Admin reads every section, each nullable field
// set or null, as the specification describes it.
func TestGetSystemStatus(t *testing.T) {
	x, f := newSystemStatusAPI(t)
	f.status = systemstatus.Status{
		Version: "1.4.0", Commit: "abc1234",
		Replicas: []systemstatus.Replica{{ID: "muster-a", Leader: true, KeyIDs: []string{"k-1"}, RefreshedAt: t0},
			{ID: "muster-b", KeyIDs: []string{}, RefreshedAt: t0}},
		Recovery: systemstatus.Recovery{Active: true, PeriodFrom: ptr(t0.Add(-time.Hour)), PeriodTo: ptr(t0),
			Until: ptr(t0.Add(15 * time.Minute))},
		DeliveryQueues: []systemstatus.Queue{{Destination: systemstatus.Entity{ID: "DSAAAAAAAAAAAA", Name: "alerts"},
			Queued: 3}},
		BrokenDestinations: []systemstatus.BrokenDestination{{
			Destination: systemstatus.Entity{ID: "DSAAAAAAAAAAAA", Name: "alerts"}, Since: t0, Reason: "answered 403"}},
		IntegrationsAttention: []systemstatus.IntegrationAttention{
			{Integration: systemstatus.Entity{ID: "NTAAAAAAAAAAAA", Name: "lab"}, Issue: systemstatus.IssueHeartbeatLost,
				Since: ptr(t0)},
			{Integration: systemstatus.Entity{ID: "NTAAAAAAAAAAAB", Name: "eu"},
				Issue: systemstatus.IssueSnapshotTruncated}},
		TemplateErrors: []systemstatus.TemplateError{{SubjectKind: systemstatus.SubjectDestination,
			Subject: systemstatus.Entity{ID: "DSAAAAAAAAAAAB", Name: "hook"}, Template: "webhook_request", Since: t0,
			Error: "line 1: boom"}},
		ActiveStorms: []systemstatus.Storm{{Route: systemstatus.Entity{ID: "RTAAAAAAAAAAAA", Name: "t"}, Since: t0,
			AlertGroupCount: 12}},
		OutgoingHeartbeat: systemstatus.OutgoingHeartbeat{Configured: true, LastAttemptAt: ptr(t0),
			LastOK: ptr(false), LastError: ptr("answered 503 (transient)")},
	}
	a := x.call(t, http.MethodGet, "/api/v1/system-status", "", "Cookie", adminCookie)
	if a.status != http.StatusOK {
		t.Fatalf("admin = %d %s", a.status, a.body)
	}
	body := string(a.body)
	for _, want := range []string{`"version":"1.4.0"`, `"commit":"abc1234"`,
		`{"id":"muster-a","key_ids":["k-1"],"leader":true,"refreshed_at":"2026-10-05T12:00:00Z"}`,
		`"key_ids":[]`, `"recovery":{"active":true,"period_from":"2026-10-05T11:00:00Z",` +
			`"period_to":"2026-10-05T12:00:00Z","until":"2026-10-05T12:15:00Z"}`,
		`"delivery_queues":[{"destination":{"id":"DSAAAAAAAAAAAA","name":"alerts"},"queued":3}]`,
		`"reason":"answered 403"`, `"issue":"heartbeat_lost","since":"2026-10-05T12:00:00Z"`,
		`"issue":"snapshot_truncated","since":null`, `"subject_kind":"destination"`, `"template":"webhook_request"`,
		`"alert_group_count":12`,
		`"outgoing_heartbeat":{"configured":true,"last_attempt_at":"2026-10-05T12:00:00Z",` +
			`"last_error":"answered 503 (transient)","last_ok":false}`} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %s:\n%s", want, body)
		}
	}
	f.status = systemstatus.Status{Version: "0.0.0-dev"}
	a = x.call(t, http.MethodGet, "/api/v1/system-status", "", "Cookie", adminCookie)
	body = string(a.body)
	for _, want := range []string{`"replicas":[]`, `"broken_destinations":[]`, `"active_storms":[]`,
		`"recovery":{"active":false,"period_from":null,"period_to":null,"until":null}`,
		`"outgoing_heartbeat":{"configured":false,"last_attempt_at":null,"last_error":null,"last_ok":null}`} {
		if a.status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("an empty status lacks %s: %d %s", want, a.status, body)
		}
	}
	if strings.Contains(body, `"commit"`) {
		t.Errorf("an empty commit is shown: %s", body)
	}
	f.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, "/api/v1/system-status", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
}

// TestGetSystemStatusForbidden covers C-19.AC-6: a Responder or a Viewer gets 403, and no credentials 401.
func TestGetSystemStatusForbidden(t *testing.T) {
	x, _ := newSystemStatusAPI(t)
	for _, cookie := range []string{responderCookie, viewerCookie} {
		a := x.call(t, http.MethodGet, "/api/v1/system-status", "", "Cookie", cookie)
		if a.status != http.StatusForbidden || !strings.Contains(string(a.body), "system-status:read") {
			t.Errorf("%s = %d %s", cookie, a.status, a.body)
		}
	}
	if a := x.call(t, http.MethodGet, "/api/v1/system-status", ""); a.status != http.StatusUnauthorized {
		t.Errorf("without credentials = %d", a.status)
	}
}

// TestSystemStatusOffersNoChange covers C-19.FR-10: the System status has no operation that changes anything.
func TestSystemStatusOffersNoChange(t *testing.T) {
	x, _ := newSystemStatusAPI(t)
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		a := x.call(t, method, "/api/v1/system-status", "{}", "Cookie", adminCookie)
		if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "GET" {
			t.Errorf("%s = %d %s", method, a.status, a.header.Get("Allow"))
		}
	}
}
