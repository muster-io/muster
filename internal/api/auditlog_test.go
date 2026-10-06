// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
)

// fakeAuditLog stands for the reader of internal/audit.
type fakeAuditLog struct {
	filter audit.Filter
	page   audit.Page
	err    error
}

func (f *fakeAuditLog) List(_ context.Context, fl audit.Filter) (audit.Page, error) {
	f.filter = fl
	return f.page, f.err
}

const auditorCookie = "auditor-cookie"

// newAuditAPI adds a session whose Role holds audit-log:read, as an Admin's does.
func newAuditAPI(t *testing.T) (*testAPI, *fakeAuditLog) {
	t.Helper()
	x := newTestAPI(t)
	roles["auditor"] = []auth.Permission{"audit-log:read"}
	t.Cleanup(func() { delete(roles, "auditor") })
	auditor := user(9, "SRDDDDDDDDDDDD", "auditor", "auditor")
	x.users.users[9] = auditor
	x.sessions.sessions[auditorCookie] = session(9, auditor, auth.StateActive)
	x.sessions.cookies[9] = auditorCookie
	fl := &fakeAuditLog{}
	x.srv.auditLog = fl
	return x, fl
}

// TestListAuditLog is C-03.FR-14 and FR-15 at the API: the filters reach the reader, entries carry the actor with its
// current name, the token, the resource, the diff with Secrets only marked and the details; next_cursor reads back as
// the position after the last entry.
func TestListAuditLog(t *testing.T) {
	x, fl := newAuditAPI(t)
	at := t0.Add(90 * time.Second)
	fl.page = audit.Page{Entries: []audit.Listed{
		{
			PublicID: "AE0000000000AA", At: at, Transport: audit.TransportUI, Action: "user.deleted",
			Actor:        audit.ListedActor{Kind: audit.ActorUser, PublicID: "SRAAAAAAAAAAAA", Name: "deleted-user-SRAAAAAAAAAAAA"},
			ResourceType: "user", ResourceID: "SRBBBBBBBBBBBB", ResourceName: "bob", TokenPublicID: "PT0000000000AA",
			TokenName: "laptop",
			Diff: []audit.Change{{Pointer: "/status", Before: "active", After: "deleted"},
				{Pointer: "/password", SecretChanged: true, Before: "never shown"}},
			Details: map[string]any{"sessions_ended": 2.0},
		},
		{
			PublicID: "AE0000000000AB", At: t0, Transport: audit.TransportSystem, Action: "user.created",
			Actor: audit.ListedActor{Kind: audit.ActorBootstrap, Name: audit.BootstrapName}, Diff: []audit.Change{},
			Details: map[string]any{},
		},
	}, Next: &audit.Cursor{At: t0, ID: 42}}
	q := url.Values{"from": {"2026-10-05T00:00:00Z"}, "to": {"2026-10-06T00:00:00Z"}, "actor": {"SRAAAAAAAAAAAA"},
		"action": {"user.deleted"}, "resource_type": {"user"}, "resource_id": {"SRBBBBBBBBBBBB"}, "limit": {"2"}}
	a := x.call(t, http.MethodGet, "/api/v1/audit-log?"+q.Encode(), "", "Cookie", auditorCookie)
	if a.status != http.StatusOK {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	f := fl.filter
	if !f.From.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) || !f.To.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0,
		time.UTC)) || f.Actor != "SRAAAAAAAAAAAA" || f.Action != "user.deleted" || f.ResourceType != "user" ||
		f.ResourceID != "SRBBBBBBBBBBBB" || f.Limit != 2 || f.After != nil {
		t.Errorf("filter = %+v", f)
	}
	var body struct {
		Items []struct {
			ID     string `json:"id"`
			At     string `json:"at"`
			Action string `json:"action"`
			Actor  struct {
				Kind string  `json:"kind"`
				ID   *string `json:"id"`
				Name string  `json:"name"`
			} `json:"actor"`
			TokenID      *string           `json:"token_id"`
			TokenName    *string           `json:"token_name"`
			ResourceID   *string           `json:"resource_id"`
			ResourceType *string           `json:"resource_type"`
			Diff         []json.RawMessage `json:"diff"`
			Details      map[string]any    `json:"details"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(a.body, &body); err != nil {
		t.Fatal(err)
	}
	first, second := body.Items[0], body.Items[1]
	if first.At != "2026-10-05T12:01:30Z" || first.Actor.Name != "deleted-user-SRAAAAAAAAAAAA" ||
		*first.Actor.ID != "SRAAAAAAAAAAAA" || *first.TokenID != "PT0000000000AA" || *first.TokenName != "laptop" ||
		*first.ResourceID != "SRBBBBBBBBBBBB" || first.Details["sessions_ended"] != 2.0 ||
		string(first.Diff[1]) != `{"pointer":"/password","secret_changed":true}` {
		t.Errorf("first = %+v, diff %s", first, first.Diff)
	}
	if second.Actor.Kind != "bootstrap" || second.Actor.Name != "bootstrap" || second.Actor.ID != nil ||
		second.TokenID != nil || second.ResourceType != nil || len(second.Diff) != 0 {
		t.Errorf("second = %+v", second)
	}
	if body.NextCursor == nil {
		t.Fatal("no next_cursor")
	}
	fl.page = audit.Page{Entries: []audit.Listed{}}
	a = x.call(t, http.MethodGet, "/api/v1/audit-log?cursor="+*body.NextCursor, "", "Cookie", auditorCookie)
	if a.status != http.StatusOK || fl.filter.After == nil || *fl.filter.After != (audit.Cursor{At: t0, ID: 42}) ||
		fl.filter.Limit != 50 || fl.filter.From != nil || a.json(t)["next_cursor"] != nil {
		t.Errorf("second page = %d %s, filter %+v", a.status, a.body, fl.filter)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/audit-log?cursor="+encodeCursor(usersCursor, userKey{}), "", "Cookie",
		auditorCookie); a.status != http.StatusBadRequest {
		t.Errorf("a cursor of the users list = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/audit-log?from=yesterday", "", "Cookie", auditorCookie); a.status != http.StatusBadRequest {
		t.Errorf("a malformed from = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/audit-log", "", "Cookie", adminCookie); a.status != http.StatusForbidden {
		t.Errorf("without audit-log:read = %d", a.status)
	}
	fl.err = errors.New("boom")
	if a = x.call(t, http.MethodGet, "/api/v1/audit-log", "", "Cookie", auditorCookie); a.status != http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
}
