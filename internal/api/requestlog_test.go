// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/requestlog"
)

// fakeRequestLog stands for the Request log reader of internal/requestlog.
type fakeRequestLog struct {
	filters []requestlog.Filter
	page    requestlog.Page
	listErr error
	got     []string
	record  requestlog.Record
	getErr  error
}

func (f *fakeRequestLog) List(_ context.Context, fl requestlog.Filter) (requestlog.Page, error) {
	f.filters = append(f.filters, fl)
	return f.page, f.listErr
}

func (f *fakeRequestLog) Get(_ context.Context, publicID string) (requestlog.Record, error) {
	f.got = append(f.got, publicID)
	return f.record, f.getErr
}

const (
	connReaderCookie = "conn-reader-cookie"
	destReaderCookie = "dest-reader-cookie"
	bothReaderCookie = "both-reader-cookie"
)

// newRequestLogAPI adds sessions whose Roles hold connections:read only, destinations:read only and both.
func newRequestLogAPI(t *testing.T) (*testAPI, *fakeRequestLog) {
	t.Helper()
	x := newTestAPI(t)
	for i, r := range []struct {
		role, cookie string
		perms        []auth.Permission
	}{
		{"conn-reader", connReaderCookie, []auth.Permission{"connections:read"}},
		{"dest-reader", destReaderCookie, []auth.Permission{"destinations:read"}},
		{"both-reader", bothReaderCookie, []auth.Permission{"connections:read", "destinations:read"}},
	} {
		roles[r.role] = r.perms
		t.Cleanup(func() { delete(roles, r.role) })
		id := int64(20 + i)
		u := user(id, "SRDDDDDDDDDDD"+string(rune('A'+i)), r.role, r.role)
		x.users.users[id] = u
		x.sessions.sessions[r.cookie] = session(id, u, auth.StateActive)
		x.sessions.cookies[id] = r.cookie
	}
	fl := &fakeRequestLog{}
	x.srv.requestLog = fl
	return x, fl
}

// problemCode is the code of the first item of a validation problem.
func problemCode(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(a.body, &p); err != nil || len(p.Errors) == 0 {
		return ""
	}
	return p.Errors[0].Pointer + " " + p.Errors[0].Code
}

// TestListRequestLogRefusals is C-23.FR-5: exactly one of connection and destination (422 one_of_required otherwise,
// before anything is read), the Permission of the kind of owner named (403), then 404 for an unknown or deleted owner;
// a caller with neither Permission is refused by the middleware.
func TestListRequestLogRefusals(t *testing.T) {
	x, fl := newRequestLogAPI(t)
	for _, q := range []string{"", "?connection=CN0000000000AA&destination=DS0000000000AA", "?direction=outbound"} {
		a := x.call(t, http.MethodGet, "/api/v1/request-log"+q, "", "Cookie", bothReaderCookie)
		if a.status != http.StatusUnprocessableEntity || problemCode(t, a) != "/query/connection one_of_required" {
			t.Errorf("%q = %d %s", q, a.status, a.body)
		}
	}
	// The kind of the owner decides the Permission; it is checked before the owner is looked up.
	for _, c := range []struct {
		cookie, query string
		want          int
	}{
		{destReaderCookie, "?connection=CN0000000000AA", http.StatusForbidden},
		{connReaderCookie, "?destination=DS0000000000AA", http.StatusForbidden},
		{"viewer-cookie", "?connection=CN0000000000AA", http.StatusForbidden},
		{connReaderCookie, "?connection=CN0000000000AA", http.StatusOK},
		{destReaderCookie, "?destination=DS0000000000AA", http.StatusOK},
	} {
		fl.filters = nil
		a := x.call(t, http.MethodGet, "/api/v1/request-log"+c.query, "", "Cookie", c.cookie)
		if a.status != c.want || (c.want == http.StatusForbidden) != (len(fl.filters) == 0) {
			t.Errorf("%s %s = %d %s, %d reads", c.cookie, c.query, a.status, a.body, len(fl.filters))
		}
	}
	a := x.call(t, http.MethodGet, "/api/v1/request-log?destination=DS0000000000AA", "", "Cookie", connReaderCookie)
	if !strings.Contains(string(a.body), "destinations:read") {
		t.Errorf("the 403 names %s", a.body)
	}
	fl.listErr = requestlog.ErrNotFound
	for _, q := range []string{"?connection=CN0000000000AA", "?destination=DS0000000000AA"} {
		a := x.call(t, http.MethodGet, "/api/v1/request-log"+q, "", "Cookie", bothReaderCookie)
		if a.status != http.StatusNotFound || a.json(t)["type"] != problemBase+typeNotFound {
			t.Errorf("%s of an unknown owner = %d %s", q, a.status, a.body)
		}
	}
	fl.listErr = requestlog.ErrOneOfRequired
	if a := x.call(t, http.MethodGet, "/api/v1/request-log?connection=CN0000000000AA", "", "Cookie",
		bothReaderCookie); a.status != http.StatusUnprocessableEntity {
		t.Errorf("the reader's one_of_required = %d", a.status)
	}
	fl.listErr = errors.New("boom")
	if a := x.call(t, http.MethodGet, "/api/v1/request-log?connection=CN0000000000AA", "", "Cookie",
		bothReaderCookie); a.status != http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
	for _, q := range []string{"?connection=CN0000000000AA&status_class=6xx",
		"?connection=CN0000000000AA&direction=sideways", "?connection=nope", "?connection=CN0000000000AA&limit=501",
		"?connection=CN0000000000AA&operation=" + strings.Repeat("x", 65), "?connection=CN0000000000AA&alert_group=x",
		"?connection=CN0000000000AA&cursor=" + encodeCursor(auditCursor, auditKey{})} {
		if a := x.call(t, http.MethodGet, "/api/v1/request-log"+q, "", "Cookie", bothReaderCookie); a.status !=
			http.StatusBadRequest {
			t.Errorf("%s = %d %s", q, a.status, a.body)
		}
	}
}

// TestListRequestLog is C-23.FR-5: the filters reach the reader, the page maps every field — nulls included — and
// next_cursor reads back as the position after the last record, null at the end.
func TestListRequestLog(t *testing.T) {
	x, fl := newRequestLogAPI(t)
	at := t0.Add(90 * time.Second)
	status, run, conn, class := 500, "CR0000000000AA", "CN0000000000AA", "transient"
	since, reason := t0, "unavailable"
	fl.page = requestlog.Page{Records: []requestlog.Summary{
		{ID: 7, PublicID: "RQ0000000000AA", At: at, Direction: requestlog.DirectionOutbound, Operation: "event",
			Method: http.MethodPost, URL: "http://hook.example.org/x?k=[redacted]", Status: &status, LatencyMS: 12,
			Via: "proxy", Attempt: 2, ErrorClass: &class, CheckRun: &run, Connection: &conn,
			Destination: &requestlog.DestinationRef{PublicID: "DS0000000000AA", Name: "hook", Type: "webhook",
				Health: "broken", BrokenSince: &since, BrokenReason: &reason},
			AlertGroup: &requestlog.AlertGroupRef{PublicID: "AG0000000000AA", Number: 41}, RequestBodyTruncated: true},
		{ID: 6, PublicID: "RQ0000000000AB", At: t0, Direction: requestlog.DirectionOutbound, Operation: "getMe",
			Method: http.MethodGet, URL: "https://api.telegram.org/bot[redacted]/getMe", Via: "direct", Attempt: 1,
			ErrorClass: new("transient"), ErrorCode: new("connect_failed"), ResponseBodyTruncated: true},
	}, Next: &requestlog.Position{At: t0, ID: 6}}
	q := url.Values{"destination": {"ds0000000000aa"}, "direction": {"outbound"}, "status_class": {"none"},
		"operation": {"createPost"}, "alert_group": {"AG0000000000AA"}, "limit": {"2"}}
	a := x.call(t, http.MethodGet, "/api/v1/request-log?"+q.Encode(), "", "Cookie", destReaderCookie)
	if a.status != http.StatusOK {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	f := fl.filters[0]
	if f.Destination != "ds0000000000aa" || f.Connection != "" || f.Direction != "outbound" || f.StatusClass != "none" ||
		f.Operation != "createPost" || f.AlertGroup != "AG0000000000AA" || f.Limit != 2 || f.After != nil {
		t.Errorf("filter = %+v", f)
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(a.body, &body); err != nil {
		t.Fatal(err)
	}
	first, second := body.Items[0], body.Items[1]
	d, _ := first["destination"].(map[string]any)
	h, _ := d["health"].(map[string]any)
	g, _ := first["alert_group"].(map[string]any)
	if first["id"] != "RQ0000000000AA" || first["at"] != "2026-10-05T12:01:30Z" || first["status"] != 500.0 ||
		first["latency_ms"] != 12.0 || first["via"] != "proxy" || first["attempt"] != 2.0 ||
		first["error_class"] != "transient" || first["error_code"] != nil || first["check"] != nil ||
		first["check_run"] != "CR0000000000AA" || first["connection_id"] != "CN0000000000AA" ||
		d["id"] != "DS0000000000AA" || d["type"] != "webhook" || h["state"] != "broken" || h["reason"] != reason ||
		g["number"] != 41.0 || first["request_body_truncated"] != true || first["response_body_truncated"] != false {
		t.Errorf("first = %v", first)
	}
	for _, k := range []string{"status", "check_run", "connection_id", "destination", "alert_group"} {
		if v, ok := second[k]; !ok || v != nil {
			t.Errorf("second %s = %v, present %v", k, v, ok)
		}
	}
	if second["error_code"] != "connect_failed" || second["response_body_truncated"] != true {
		t.Errorf("second = %v", second)
	}
	if body.NextCursor == nil {
		t.Fatal("no next_cursor")
	}
	fl.page = requestlog.Page{Records: []requestlog.Summary{}}
	a = x.call(t, http.MethodGet, "/api/v1/request-log?connection=CN0000000000AA&status_class=5xx&cursor="+
		*body.NextCursor, "", "Cookie", connReaderCookie)
	f = fl.filters[1]
	if a.status != http.StatusOK || f.After == nil || !f.After.At.Equal(t0) || f.After.ID != 6 || f.Limit != 50 ||
		f.Connection != "CN0000000000AA" || f.StatusClass != "5xx" || a.json(t)["next_cursor"] != nil {
		t.Errorf("second page = %d %s, filter %+v", a.status, a.body, f)
	}
	if items, _ := a.json(t)["items"].([]any); items == nil || len(items) != 0 {
		t.Errorf("an empty page = %s", a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/request-log?connection=CN0000000000AA&limit=900", "", "Cookie",
		connReaderCookie); a.status != http.StatusBadRequest {
		t.Errorf("a limit past the spec = %d", a.status)
	}
}

// TestGetRequestRecord is C-23.FR-5: a record reads with its headers and bodies; one past a bound, unknown or of a
// deleted owner is 404 for everyone; the Permission is that of the record's owner — destinations:read for an outgoing
// webhook's, connections:read for a Connection's and for one without an owner.
func TestGetRequestRecord(t *testing.T) {
	x, fl := newRequestLogAPI(t)
	status, body, errText := 201, `{"id":"p1"}`, "masked"
	fl.record = requestlog.Record{Summary: requestlog.Summary{ID: 3, PublicID: "RQ0000000000AA", At: t0,
		Direction: requestlog.DirectionOutbound, Operation: "createPost", Method: http.MethodPost,
		URL: "http://mm.example.org/api/v4/posts", Status: &status, LatencyMS: 4, Via: "direct", Attempt: 1},
		Owner:          requestlog.OwnerConnection,
		RequestHeaders: map[string][]string{"Authorization": {"[redacted]"}, "User-Agent": {"Muster/1"}},
		RequestBody:    `{"message":"x"}`, RequestBodyBytes: 15, ResponseHeaders: map[string][]string{"X": {"y"}},
		ResponseBody: &body, ResponseBodyBytes: 11, Error: &errText}
	a := x.call(t, http.MethodGet, "/api/v1/request-log/rq0000000000aa", "", "Cookie", connReaderCookie)
	if a.status != http.StatusOK {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	m := a.json(t)
	rh, _ := m["request_headers"].(map[string]any)
	if fl.got[0] != "rq0000000000aa" || m["id"] != "RQ0000000000AA" || m["operation"] != "createPost" ||
		m["request_body"] != `{"message":"x"}` || m["request_body_bytes"] != 15.0 || m["response_body"] != body ||
		m["response_body_bytes"] != 11.0 || m["error"] != errText || m["status"] != 201.0 ||
		rh["Authorization"].([]any)[0] != "[redacted]" || m["response_headers"] == nil {
		t.Errorf("record = %s", a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AA", "", "Cookie", destReaderCookie); a.status !=
		http.StatusForbidden || !strings.Contains(string(a.body), "connections:read") {
		t.Errorf("a Connection's record without connections:read = %d %s", a.status, a.body)
	}
	fl.record = requestlog.Record{Summary: requestlog.Summary{PublicID: "RQ0000000000AB", At: t0,
		Direction: requestlog.DirectionOutbound, Operation: "event", Method: http.MethodPost, URL: "http://h/x",
		Via: "direct", Attempt: 1, ErrorClass: new("transient"), ErrorCode: new("connect_failed")},
		Owner: requestlog.OwnerDestination, RequestHeaders: nil, Error: &errText}
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AB", "", "Cookie", connReaderCookie); a.status !=
		http.StatusForbidden || !strings.Contains(string(a.body), "destinations:read") {
		t.Errorf("an outgoing webhook's record without destinations:read = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AB", "", "Cookie", destReaderCookie)
	m = a.json(t)
	if a.status != http.StatusOK || m["response_headers"] != nil || m["response_body"] != nil || m["status"] != nil ||
		m["error_code"] != "connect_failed" {
		t.Errorf("a record without an answer = %d %s", a.status, a.body)
	}
	if rh, ok := m["request_headers"].(map[string]any); !ok || len(rh) != 0 {
		t.Errorf("no request headers = %v", m["request_headers"])
	}
	fl.record.Owner = requestlog.OwnerNone
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AB", "", "Cookie", destReaderCookie); a.status !=
		http.StatusForbidden {
		t.Errorf("a record without an owner, destinations:read only = %d", a.status)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AB", "", "Cookie", connReaderCookie); a.status !=
		http.StatusOK {
		t.Errorf("a record without an owner, connections:read = %d", a.status)
	}
	fl.getErr = requestlog.ErrNotFound
	for _, cookie := range []string{connReaderCookie, destReaderCookie, bothReaderCookie} {
		a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AC", "", "Cookie", cookie)
		if a.status != http.StatusNotFound || !strings.Contains(string(a.body), "No such request record") {
			t.Errorf("%s: a record past a bound = %d %s", cookie, a.status, a.body)
		}
	}
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AC", "", "Cookie", "viewer-cookie"); a.status !=
		http.StatusForbidden {
		t.Errorf("neither Permission = %d", a.status)
	}
	fl.getErr = errors.New("boom")
	if a := x.call(t, http.MethodGet, "/api/v1/request-log/RQ0000000000AC", "", "Cookie", bothReaderCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
}

// TestListRequestLogByRun is C-23.FR-5 for check runs: a run alone needs connections:read, checked before anything is
// read, and reaches the reader alone; beside a Connection or a Destination it narrows that owner's list under that
// owner's Permission; both owners with a run, or a filter without an owner or a run, are 422 one_of_required; a run
// that is no public_id is 400.
func TestListRequestLogByRun(t *testing.T) {
	x, fl := newRequestLogAPI(t)
	for _, c := range []struct {
		cookie, query string
		want          int
		filter        requestlog.Filter
	}{
		{connReaderCookie, "?run=CR0000000000AA", http.StatusOK, requestlog.Filter{Run: "CR0000000000AA"}},
		{destReaderCookie, "?run=CR0000000000AA", http.StatusForbidden, requestlog.Filter{}},
		{"viewer-cookie", "?run=CR0000000000AA", http.StatusForbidden, requestlog.Filter{}},
		{destReaderCookie, "?destination=DS0000000000AA&run=CR0000000000AA", http.StatusOK,
			requestlog.Filter{Destination: "DS0000000000AA", Run: "CR0000000000AA"}},
		{connReaderCookie, "?connection=CN0000000000AA&run=CR0000000000AA", http.StatusOK,
			requestlog.Filter{Connection: "CN0000000000AA", Run: "CR0000000000AA"}},
		{connReaderCookie, "?destination=DS0000000000AA&run=CR0000000000AA", http.StatusForbidden,
			requestlog.Filter{}},
		{bothReaderCookie, "?connection=CN0000000000AA&destination=DS0000000000AA&run=CR0000000000AA",
			http.StatusUnprocessableEntity, requestlog.Filter{}},
		{bothReaderCookie, "?direction=inbound", http.StatusUnprocessableEntity, requestlog.Filter{}},
		{bothReaderCookie, "?run=nope", http.StatusBadRequest, requestlog.Filter{}},
	} {
		fl.filters = nil
		a := x.call(t, http.MethodGet, "/api/v1/request-log"+c.query, "", "Cookie", c.cookie)
		read := c.want == http.StatusOK
		if a.status != c.want || read != (len(fl.filters) == 1) {
			t.Errorf("%s %s = %d %s, %d reads", c.cookie, c.query, a.status, a.body, len(fl.filters))
			continue
		}
		if read {
			got := fl.filters[0]
			got.Limit = 0
			if got != c.filter {
				t.Errorf("%s: filter %+v", c.query, got)
			}
		}
		switch c.want {
		case http.StatusForbidden:
			need := "connections:read"
			if strings.Contains(c.query, "destination=") {
				need = "destinations:read"
			}
			if c.cookie != "viewer-cookie" && !strings.Contains(string(a.body), need) {
				t.Errorf("%s: the 403 names %s", c.query, a.body)
			}
		case http.StatusUnprocessableEntity:
			if problemCode(t, a) != "/query/connection one_of_required" {
				t.Errorf("%s: %s", c.query, a.body)
			}
		}
	}
}

// TestCheckResultsNameTheirRun is C-23.FR-5: checkConnection, checkConnectionDraft, checkDestination and
// testDestination answer the check run of their requests as request_log_run.
func TestCheckResultsNameTheirRun(t *testing.T) {
	const run = "CRAAAAAAAAAAA2"
	x, fc := newConnectionsAPI(t)
	fc.check = connections.CheckResult{OK: true, Run: run}
	for _, path := range []string{"/api/v1/connections/" + connectionID + "/checks", "/api/v1/connections/checks"} {
		body := ""
		if path == "/api/v1/connections/checks" {
			body = `{"type":"mattermost","server_url":"http://127.0.0.1:18066"}`
		}
		a := x.as(t, connectionsWriter, http.MethodPost, path, body)
		var res gen.ConnectionCheckResult
		decodeInto(t, a, &res)
		if a.status != http.StatusOK || res.RequestLogRun != run {
			t.Errorf("%s = %d %s", path, a.status, a.body)
		}
	}

	y, fd, _ := newDestinationsAPI(t)
	ft := y.srv.tokens.(*fakeTokens)
	ft.idents[destinationsTester] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"destinations:read", "destinations:test"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 53, Name: "destinations-test"}}
	fd.check = destinations.CheckResult{OK: true, Health: destinations.Health{State: "healthy"}, Run: run}
	a := y.as(t, destinationsTester, http.MethodPost, "/api/v1/destinations/"+mattermostID+"/checks", "")
	var checked gen.DestinationCheckResult
	decodeInto(t, a, &checked)
	if a.status != http.StatusOK || checked.RequestLogRun != run {
		t.Errorf("checkDestination = %d %s", a.status, a.body)
	}
	fd.test = destinations.TestResult{Health: destinations.Health{State: "healthy"}, Run: run}
	a = y.as(t, destinationsTester, http.MethodPost, "/api/v1/destinations/"+webhookID+"/tests",
		`{"source":{"kind":"example"}}`)
	var tested gen.DestinationTestResult
	decodeInto(t, a, &tested)
	if a.status != http.StatusOK || tested.RequestLogRun != run {
		t.Errorf("testDestination = %d %s", a.status, a.body)
	}
}
