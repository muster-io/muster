// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
)

const (
	integrationID = "NTAAAAAAAAAAAA"
	snapshotID    = "SSAAAAAAAAAAAA"
)

// fakeIntegrations stands for internal/integrations: one Integration, prod-eu, until it is deleted.
type fakeIntegrations struct {
	in       integrations.Integration
	deleted  bool
	page     integrations.Page
	filter   integrations.ListFilter
	inputs   []integrations.Input
	versions []*int64
	by       []integrations.Requester
	tokens   []integrations.Token
	names    []string
	revoked  []string
	err      error
}

func newFakeIntegrations() *fakeIntegrations {
	at := t0.Add(time.Minute)
	return &fakeIntegrations{in: integrations.Integration{
		ID: 1, PublicID: integrationID, Name: "prod-eu", ConnectionMode: "webhook_only",
		StaticLabels: map[string]string{"cluster": "prod-eu"}, DuplicateWindowSeconds: 45,
		Heartbeat:      integrations.Heartbeat{TimeoutSeconds: 300, State: "not_configured"},
		LastSnapshotAt: &at, CreatedAt: t0, Version: 1,
	}}
}

func (f *fakeIntegrations) List(_ context.Context, fl integrations.ListFilter) (integrations.Page, error) {
	f.filter = fl
	return f.page, f.err
}

func (f *fakeIntegrations) Get(_ context.Context, id string) (integrations.Integration, error) {
	if f.deleted || id != integrationID {
		return integrations.Integration{}, integrations.ErrNotFound
	}
	return f.in, f.err
}

func (f *fakeIntegrations) Create(_ context.Context, r integrations.Requester, in integrations.Input) (
	integrations.Integration, error) {
	f.by, f.inputs = append(f.by, r), append(f.inputs, in)
	if in.Heartbeat.Enabled {
		return integrations.Integration{}, &integrations.FieldError{Pointer: "/heartbeat/enabled",
			Code: integrations.CodeUnsupported, Detail: "The Heartbeat cannot be switched on yet."}
	}
	if in.Name == "taken" {
		return integrations.Integration{}, integrations.ErrNameTaken
	}
	out := f.in
	out.Name, out.StaticLabels = in.Name, in.StaticLabels
	return out, f.err
}

func (f *fakeIntegrations) Update(_ context.Context, r integrations.Requester, _ string, version *int64,
	in integrations.Input) (integrations.Integration, error) {
	f.by, f.inputs, f.versions = append(f.by, r), append(f.inputs, in), append(f.versions, version)
	if version != nil && *version != f.in.Version {
		return integrations.Integration{}, integrations.ErrVersionMismatch
	}
	f.in.Name, f.in.Version = in.Name, f.in.Version+1
	return f.in, f.err
}

func (f *fakeIntegrations) Delete(_ context.Context, r integrations.Requester, _ string, version *int64) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if version != nil && *version != f.in.Version {
		return integrations.ErrVersionMismatch
	}
	f.deleted = true
	return f.err
}

func (f *fakeIntegrations) ListTokens(context.Context, string) ([]integrations.Token, error) {
	if f.deleted {
		return nil, integrations.ErrNotFound
	}
	return f.tokens, f.err
}

func (f *fakeIntegrations) CreateToken(_ context.Context, r integrations.Requester, _, name string) (
	integrations.CreatedToken, error) {
	f.by, f.names = append(f.by, r), append(f.names, name)
	t := integrations.Token{ID: 9, PublicID: "NKAAAAAAAAAAAA", Name: name, CreatedAt: t0}
	return integrations.CreatedToken{Token: t, Value: "mstr_int_new",
		Snippet: integrations.Snippet("prod-eu", f.IngestURL(), "mstr_int_new")}, f.err
}

func (f *fakeIntegrations) RevokeToken(_ context.Context, r integrations.Requester, _, tokenID string) error {
	f.by, f.revoked = append(f.by, r), append(f.revoked, tokenID)
	return f.err
}

func (f *fakeIntegrations) IngestURL() string { return "http://localhost:8081/api/v1/ingest" }

func (f *fakeIntegrations) HeartbeatURL() string { return "http://localhost:8081/api/v1/heartbeat" }

// fakeSnapshots stands for the Stored Snapshot reads of internal/ingest.
type fakeSnapshots struct {
	page     ingest.Page
	filter   ingest.ListFilter
	snapshot ingest.Snapshot
	err      error
}

func (f *fakeSnapshots) List(_ context.Context, fl ingest.ListFilter) (ingest.Page, error) {
	f.filter = fl
	return f.page, f.err
}

func (f *fakeSnapshots) Get(_ context.Context, id string) (ingest.Snapshot, error) {
	if id != snapshotID {
		return ingest.Snapshot{}, ingest.ErrNotFound
	}
	return f.snapshot, f.err
}

func newIntegrationsAPI(t *testing.T) (*testAPI, *fakeIntegrations, *fakeSnapshots) {
	t.Helper()
	x, _, _, _ := newTokensAPI(t)
	fi, fs := newFakeIntegrations(), &fakeSnapshots{}
	x.srv.integrations, x.srv.snapshots = fi, fs
	return x, fi, fs
}

const integrationBody = `{"name":"prod-eu","connection_mode":"webhook_only","static_labels":{"cluster":"prod-eu"},
	"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`

// TestIntegrationsAPI is C-05.FR-1, FR-6 and FR-8 at the API: the Integration's fields and URLs, ETag and Location on
// creation, If-Match on update (428 without, 412 when stale), optional If-Match on deletion, 404 once deleted, and the
// refusals of the domain as problems.
func TestIntegrationsAPI(t *testing.T) {
	x, fi, _ := newIntegrationsAPI(t)
	a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/integrations", integrationBody)
	m := a.json(t)
	heartbeat, _ := m["heartbeat"].(map[string]any)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/integrations/"+integrationID || m["id"] != integrationID ||
		m["ingest_url"] != "http://localhost:8081/api/v1/ingest" || m["builtin"] != false ||
		m["snapshot_count"] != float64(0) || m["open_alert_group_count"] != float64(0) || m["etag"] != `"1"` ||
		heartbeat["state"] != "not_configured" || heartbeat["url"] != "http://localhost:8081/api/v1/heartbeat" ||
		m["last_snapshot_at"] != "2026-10-05T12:01:00Z" {
		t.Errorf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := fi.inputs[0]
	if in.Name != "prod-eu" || in.ConnectionMode != "webhook_only" || in.StaticLabels["cluster"] != "prod-eu" ||
		in.DuplicateWindowSeconds != 45 || in.Heartbeat.Enabled || in.Heartbeat.TimeoutSeconds != nil ||
		in.Description != nil || fi.by[0].Actor.Kind != audit.ActorUser || fi.by[0].Transport != audit.TransportUI {
		t.Errorf("input %+v by %+v", in, fi.by[0])
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/integrations",
		strings.Replace(integrationBody, `"enabled":false}`, `"enabled":true,"timeout_seconds":60}`, 1))
	var p struct {
		Errors []struct{ Pointer, Code string } `json:"errors"`
	}
	decodeInto(t, a, &p)
	if a.status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Pointer != "/heartbeat/enabled" ||
		p.Errors[0].Code != "unsupported" || *fi.inputs[1].Heartbeat.TimeoutSeconds != 60 {
		t.Errorf("heartbeat on = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/integrations",
		strings.Replace(integrationBody, `"prod-eu","connection_mode"`, `"taken","connection_mode"`, 1))
	if a.status != http.StatusConflict || a.code(t) != "name_taken" {
		t.Errorf("taken = %d %s", a.status, a.body)
	}
	for name, body := range map[string]string{
		"other mode":  strings.Replace(integrationBody, "webhook_only", "polling", 1),
		"zero window": strings.Replace(integrationBody, `"duplicate_window_seconds":45`, `"duplicate_window_seconds":0`, 1),
		"no heartbeat": strings.Replace(integrationBody, `,
	"duplicate_window_seconds":45,"heartbeat":{"enabled":false}`, `,"duplicate_window_seconds":45`, 1),
	} {
		if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/integrations", body); a.status !=
			http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	if a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID, "", "Cookie", viewerCookie); a.status !=
		http.StatusOK || a.header.Get("ETag") != `"1"` {
		t.Errorf("get by a viewer = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/integrations", integrationBody); a.status !=
		http.StatusForbidden {
		t.Errorf("create by a viewer = %d", a.status)
	}
	path := "/api/v1/integrations/" + integrationID
	if a = x.mutate(t, adminCookie, http.MethodPut, path, integrationBody); a.status != http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodPut, path, integrationBody, "If-Match", `"7"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("update with a stale If-Match = %d", a.status)
	}
	body := strings.Replace(integrationBody, `"prod-eu","connection_mode"`, `"prod-eu-1","connection_mode"`, 1)
	if a = x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `"1"`); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"2"` || a.json(t)["name"] != "prod-eu-1" {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, path, "", "If-Match", `"1"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("delete with a stale If-Match = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, path, ""); a.status != http.StatusNoContent ||
		fi.versions[len(fi.versions)-1] != nil {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, path, "", "Cookie", adminCookie); a.status != http.StatusNotFound {
		t.Errorf("get after delete = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, path+"/tokens", "", "Cookie", adminCookie); a.status != http.StatusNotFound {
		t.Errorf("tokens after delete = %d", a.status)
	}
	if a = x.call(t, http.MethodPost, "/api/v1/integrations", integrationBody, bearer(fullToken)...); a.status !=
		http.StatusCreated || fi.by[len(fi.by)-1].Transport != audit.TransportAPI {
		t.Errorf("create with a token = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/integrations", "", bearer("mstr_int_"+strings.Repeat("a", 52))...); a.status !=
		http.StatusUnauthorized {
		t.Errorf("an Integration token on the API = %d", a.status)
	}
}

// TestListIntegrationsAPI: a page with its cursor, and the cursor read back.
func TestListIntegrationsAPI(t *testing.T) {
	x, fi, _ := newIntegrationsAPI(t)
	next := int64(4)
	in := fi.in
	in.Description, in.StaticLabels, in.LastSnapshotAt = "described", nil, nil
	fi.page = integrations.Page{Integrations: []integrations.Integration{in}, Next: &next}
	a := x.call(t, http.MethodGet, "/api/v1/integrations?limit=1", "", "Cookie", viewerCookie)
	items, _ := a.json(t)["items"].([]any)
	if a.status != http.StatusOK || len(items) != 1 || fi.filter.Limit != 1 {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	item := items[0].(map[string]any)
	if item["description"] != "described" || item["last_snapshot_at"] != nil || item["warnings"] == nil {
		t.Errorf("item %v", item)
	}
	cursor, _ := a.json(t)["next_cursor"].(string)
	fi.page = integrations.Page{}
	a = x.call(t, http.MethodGet, "/api/v1/integrations?cursor="+url.QueryEscape(cursor), "", "Cookie", viewerCookie)
	if a.status != http.StatusOK || fi.filter.After == nil || *fi.filter.After != 4 || a.json(t)["next_cursor"] != nil {
		t.Errorf("page 2 = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/integrations?cursor=bad", "", "Cookie", viewerCookie); a.status !=
		http.StatusBadRequest {
		t.Errorf("bad cursor = %d", a.status)
	}
	fi.err = errors.New("down")
	if a = x.call(t, http.MethodGet, "/api/v1/integrations", "", "Cookie", viewerCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failing list = %d", a.status)
	}
}

// TestIntegrationTokensAPI is C-05.FR-2 and C-05.AC-6 at the API: the created token's value and snippet, once; the list
// without values; the revocation.
func TestIntegrationTokensAPI(t *testing.T) {
	x, fi, _ := newIntegrationsAPI(t)
	path := "/api/v1/integrations/" + integrationID + "/tokens"
	a := x.mutate(t, adminCookie, http.MethodPost, path, `{"name":"rotation-1"}`)
	m := a.json(t)
	snippet, _ := m["alertmanager_snippet"].(string)
	if a.status != http.StatusCreated || m["value"] != "mstr_int_new" || m["heartbeat_snippet"] != nil ||
		!strings.Contains(snippet, "send_resolved: true") || !strings.Contains(snippet, "max_alerts: 0") ||
		!strings.Contains(snippet, "credentials: mstr_int_new") ||
		!strings.Contains(snippet, "url: http://localhost:8081/api/v1/ingest") || fi.names[0] != "rotation-1" {
		t.Errorf("create = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, path, ""); a.status != http.StatusCreated || fi.names[1] != "" {
		t.Errorf("create without a body = %d %s", a.status, a.body)
	}
	used := t0
	fi.tokens = []integrations.Token{{PublicID: "NKAAAAAAAAAAAA", Name: "rotation-1", CreatedAt: t0, LastUsedAt: &used},
		{PublicID: "NKBBBBBBBBBBBB", CreatedAt: t0}}
	a = x.call(t, http.MethodGet, path, "", "Cookie", viewerCookie)
	if a.status != http.StatusOK || strings.Contains(string(a.body), "value") || strings.Contains(string(a.body),
		"mstr_int_") {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, path+"/NKAAAAAAAAAAAA", ""); a.status != http.StatusNoContent ||
		fi.revoked[0] != "NKAAAAAAAAAAAA" {
		t.Errorf("revoke = %d %s", a.status, a.body)
	}
	fi.err = integrations.ErrNotFound
	if a = x.mutate(t, adminCookie, http.MethodDelete, path+"/NKAAAAAAAAAAAA", ""); a.status != http.StatusNotFound {
		t.Errorf("revoke again = %d", a.status)
	}
	fi.err = &integrations.FieldError{Pointer: "/name", Code: integrations.CodeTooLong, Detail: "long"}
	if a = x.mutate(t, adminCookie, http.MethodPost, path, `{"name":"x"}`); a.status !=
		http.StatusUnprocessableEntity {
		t.Errorf("a long name = %d", a.status)
	}
}

// TestStoredSnapshotsAPI is C-05.FR-7, FR-10 and C-05.AC-8 at the API: the filters and the cursor reach the domain,
// a body is text when it is valid UTF-8 and base64 otherwise, and a Viewer or a Responder gets 403.
func TestStoredSnapshotsAPI(t *testing.T) {
	x, _, fs := newIntegrationsAPI(t)
	processed, failed, count := t0.Add(time.Second), "not JSON", int64(2)
	summary := ingest.Summary{ID: 3, PublicID: snapshotID, Integration: ingest.Ref{PublicID: integrationID,
		Name: "prod-eu"}, ReceivedAt: t0, ProcessedAt: &processed, SizeBytes: 8, State: ingest.StateFailed,
		ProcessingError: &failed, AlertCount: &count}
	fs.page = ingest.Page{Snapshots: []ingest.Summary{summary}, Next: &ingest.Position{ReceivedAt: t0, ID: 3}}
	q := "/api/v1/stored-snapshots?integration=" + integrationID + "&from=2026-10-05T00:00:00Z&to=2026-10-06T00:00:00Z" +
		"&state=pending&state=failed&limit=1"
	a := x.call(t, http.MethodGet, q, "", "Cookie", adminCookie)
	if a.status != http.StatusOK || fs.filter.Integration != integrationID || fs.filter.From == nil ||
		fs.filter.To == nil || len(fs.filter.States) != 2 || fs.filter.Limit != 1 {
		t.Fatalf("list = %d %s %+v", a.status, a.body, fs.filter)
	}
	items, _ := a.json(t)["items"].([]any)
	item := items[0].(map[string]any)
	if item["state"] != "failed" || item["processing_error"] != "not JSON" || item["group_key"] != nil ||
		item["alert_count"] != float64(2) || item["integration"].(map[string]any)["name"] != "prod-eu" {
		t.Errorf("item %v", item)
	}
	cursor, _ := a.json(t)["next_cursor"].(string)
	fs.page = ingest.Page{}
	a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots?integration="+integrationID+"&cursor="+
		url.QueryEscape(cursor), "", "Cookie", adminCookie)
	if a.status != http.StatusOK || fs.filter.After == nil || fs.filter.After.ID != 3 ||
		!fs.filter.After.ReceivedAt.Equal(t0) || a.json(t)["next_cursor"] != nil {
		t.Errorf("page 2 = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots", "", "Cookie", adminCookie); a.status !=
		http.StatusBadRequest {
		t.Errorf("without an integration = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots?integration="+integrationID+"&state=lost", "",
		"Cookie", adminCookie); a.status != http.StatusBadRequest {
		t.Errorf("an unknown state = %d", a.status)
	}

	contentType := "text/plain"
	fs.snapshot = ingest.Snapshot{Summary: summary, Body: []byte("not json"), ContentType: &contentType}
	a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots/"+snapshotID, "", "Cookie", adminCookie)
	m := a.json(t)
	if a.status != http.StatusOK || m["body"] != "not json" || m["body_encoding"] != "utf8" ||
		m["content_type"] != "text/plain" || m["state"] != "failed" || m["processing_error"] != "not JSON" {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	fs.snapshot = ingest.Snapshot{Summary: summary, Body: []byte{0xff, 0xfe}}
	a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots/"+snapshotID, "", "Cookie", adminCookie)
	m = a.json(t)
	if m["body"] != base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}) || m["body_encoding"] != "base64" ||
		m["content_type"] != nil {
		t.Errorf("binary body = %s", a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots/SS0000000000ZZ", "", "Cookie", adminCookie); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
	for _, path := range []string{"/api/v1/stored-snapshots?integration=" + integrationID,
		"/api/v1/stored-snapshots/" + snapshotID} {
		if a = x.call(t, http.MethodGet, path, "", "Cookie", viewerCookie); a.status != http.StatusForbidden {
			t.Errorf("%s by a viewer = %d", path, a.status)
		}
	}
	fs.err = errors.New("down")
	if a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots?integration="+integrationID, "", "Cookie",
		adminCookie); a.status != http.StatusInternalServerError {
		t.Errorf("failing list = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/stored-snapshots/"+snapshotID, "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failing get = %d", a.status)
	}
}

// fakeAlerts stands for the Alerts view of internal/ingest and its learned Alertmanager routes.
type fakeAlerts struct {
	page        ingest.AlertPage
	filter      ingest.AlertFilter
	err         error
	routes      []ingest.AlertmanagerRoute
	integration string
}

func (f *fakeAlerts) Routes(_ context.Context, integration string) ([]ingest.AlertmanagerRoute, error) {
	f.integration = integration
	return f.routes, f.err
}

func (f *fakeAlerts) List(_ context.Context, fl ingest.AlertFilter) (ingest.AlertPage, error) {
	f.filter = fl
	return f.page, f.err
}

// TestIntegrationAlertsAPI is C-06.FR-19 and C-05.FR-7 at the API: the Alerts view with its fields, the state,
// Matcher, text and sort parameters, the cursor of the sort it belongs to, and Matchers that do not parse.
func TestIntegrationAlertsAPI(t *testing.T) {
	x, _, _ := newIntegrationsAPI(t)
	fa := &fakeAlerts{}
	x.srv.alerts = fa
	// Every Role holds alerts:read (reference.md); the Roles of these tests predate the Alerts view.
	viewer := roles[auth.RoleViewer]
	roles[auth.RoleViewer] = append(slices.Clone(viewer), "alerts:read")
	t.Cleanup(func() { roles[auth.RoleViewer] = viewer })
	reason, text, resolvedAt := "gone", "Alertmanager no longer reports this alert", t0.Add(time.Hour)
	fa.page = ingest.AlertPage{Alerts: []ingest.ViewAlert{
		{ID: 1, Fingerprint: "0123456789abcdef", Labels: map[string]string{"instance": "db-a", "cluster": "a"},
			Annotations: map[string]string{"summary": "full"}, State: "firing", StartsAt: t0, LastSeenAt: t0,
			GroupKeys: []string{`{}:{alertname="DiskFull"}`}, Warnings: []string{"cluster"}},
		{ID: 2, Fingerprint: "1123456789abcdef", Labels: map[string]string{"instance": "db-c"}, State: "resolved",
			Reason: &reason, ReasonText: &text, ResolvedAt: &resolvedAt, StartsAt: t0, LastSeenAt: t0,
			GroupKeys: []string{}, Warnings: []string{}},
	}, Next: &ingest.AlertPosition{At: t0, ID: 2}}
	q := "/api/v1/integrations/" + integrationID + "/alerts?state=firing&q=DB&sort=starts_at&limit=2&label=" +
		url.QueryEscape(`instance=~"db-.*"`) + "&label=" + url.QueryEscape(`cluster!="b"`)
	a := x.call(t, http.MethodGet, q, "", "Cookie", viewerCookie)
	if a.status != http.StatusOK || fa.filter.Integration != integrationID || fa.filter.State != "firing" ||
		fa.filter.Query != "DB" || fa.filter.Sort != ingest.SortStarts || fa.filter.Limit != 2 ||
		len(fa.filter.Matchers) != 2 || fa.filter.Matchers[0].String() != `instance=~"db-.*"` {
		t.Fatalf("list = %d %s %+v", a.status, a.body, fa.filter)
	}
	var page gen.IntegrationAlertList
	decodeInto(t, a, &page)
	first, second := page.Items[0], page.Items[1]
	if first.Fingerprint != "0123456789abcdef" || first.State != gen.AlertStateFiring || first.Labels["instance"] != "db-a" ||
		(*first.Annotations)["summary"] != "full" || !first.ResolveReason.IsNull() || !first.ResolvedAt.IsNull() ||
		len(*first.StaticLabelWarnings) != 1 || first.AlertmanagerGroups[0] != `{}:{alertname="DiskFull"}` ||
		first.Route != nil || first.AlertGroup != nil {
		t.Errorf("first %s", a.body)
	}
	if r, _ := second.ResolveReason.Get(); r != gen.NullableResolveReasonGone || second.ResolveReasonText.MustGet() != text ||
		!second.ResolvedAt.MustGet().Equal(resolvedAt) || second.AlertmanagerGroups == nil {
		t.Errorf("second %s", a.body)
	}
	cursor := page.NextCursor.MustGet()
	fa.page = ingest.AlertPage{}
	a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts?sort=starts_at&cursor="+
		url.QueryEscape(cursor), "", "Cookie", viewerCookie)
	if a.status != http.StatusOK || fa.filter.After == nil || fa.filter.After.ID != 2 || !fa.filter.After.At.Equal(t0) ||
		a.json(t)["next_cursor"] != nil || fa.filter.Sort != ingest.SortStarts || fa.filter.State != "" {
		t.Errorf("page 2 = %d %s %+v", a.status, a.body, fa.filter)
	}
	if fa.filter = (ingest.AlertFilter{}); x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts",
		"", "Cookie", viewerCookie).status != http.StatusOK || fa.filter.Sort != ingest.SortLastSeenDesc ||
		fa.filter.Limit != 50 {
		t.Errorf("defaults %+v", fa.filter)
	}
	a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts?cursor="+url.QueryEscape(cursor), "",
		"Cookie", viewerCookie)
	if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), "invalid_cursor") {
		t.Errorf("a cursor of another sort = %d %s", a.status, a.body)
	}
	for _, tt := range []struct{ label, code, pointer string }{
		{`instance`, "invalid_format", "/query/label/0"},
		{`pod=~"api-(.*"`, "invalid_regex", "/query/label/0"},
	} {
		a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts?label=a%3D%22b%22&label="+
			url.QueryEscape(tt.label), "", "Cookie", viewerCookie)
		want := strings.Replace(tt.pointer, "0", "1", 1)
		if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), tt.code) ||
			!strings.Contains(string(a.body), want) {
			t.Errorf("label %s = %d %s", tt.label, a.status, a.body)
		}
	}
	fa.err = integrations.ErrNotFound
	if a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts", "", "Cookie",
		viewerCookie); a.status != http.StatusNotFound {
		t.Errorf("unknown integration = %d", a.status)
	}
	fa.err = errors.New("down")
	if a = x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alerts", "", "Cookie",
		viewerCookie); a.status != http.StatusInternalServerError {
		t.Errorf("failing list = %d", a.status)
	}
}

// TestIntegrationHandlersWithoutIdentity: the handlers that change something refuse to run without an identity, which
// the middleware always sets.
func TestIntegrationHandlersWithoutIdentity(t *testing.T) {
	x, _, _ := newIntegrationsAPI(t)
	ctx := t.Context()
	var errs []error
	_, err := x.srv.CreateIntegration(ctx, gen.CreateIntegrationRequestObject{})
	errs = append(errs, err)
	_, err = x.srv.UpdateIntegration(ctx, gen.UpdateIntegrationRequestObject{IntegrationId: integrationID})
	errs = append(errs, err)
	_, err = x.srv.DeleteIntegration(ctx, gen.DeleteIntegrationRequestObject{IntegrationId: integrationID})
	errs = append(errs, err)
	_, err = x.srv.CreateIntegrationToken(ctx, gen.CreateIntegrationTokenRequestObject{IntegrationId: integrationID})
	errs = append(errs, err)
	_, err = x.srv.RevokeIntegrationToken(ctx, gen.RevokeIntegrationTokenRequestObject{IntegrationId: integrationID})
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.Is(err, errUnauthenticated) {
			t.Errorf("handler %d = %v", i, err)
		}
	}
}

func decodeInto(t *testing.T, a answer, v any) {
	t.Helper()
	if err := json.Unmarshal(a.body, v); err != nil {
		t.Fatalf("%s: %v", a.body, err)
	}
}

// TestBuiltinAndWarningsAPI is C-06.FR-14 and C-06.FR-18 at the API: the built-in Integration is marked builtin and
// its refusals are 409 builtin_immutable; the warnings carry their own fields only.
func TestBuiltinAndWarningsAPI(t *testing.T) {
	x, fi, _ := newIntegrationsAPI(t)
	fi.in.Builtin = true
	fi.in.Warnings = []integrations.Warning{{Kind: integrations.WarningSnapshotTruncated, TruncatedGroupCount: 1},
		{Kind: integrations.WarningLongRepeatInterval, RoutePath: `{}/{kind="info"}`, RepeatIntervalSeconds: 7200}}
	a := x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID, "", "Cookie", viewerCookie)
	var got gen.Integration
	decodeInto(t, a, &got)
	if a.status != http.StatusOK || !got.Builtin || len(got.Warnings) != 2 ||
		got.Warnings[0].Kind != gen.IntegrationWarningKindSnapshotTruncated ||
		got.Warnings[0].TruncatedGroupCount.MustGet() != 1 || got.Warnings[0].RoutePath.IsSpecified() ||
		got.Warnings[1].RoutePath.MustGet() != `{}/{kind="info"}` || got.Warnings[1].RepeatIntervalSeconds.MustGet() != 7200 ||
		got.Warnings[1].TruncatedGroupCount.IsSpecified() {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	fi.err = integrations.ErrBuiltinImmutable
	path := "/api/v1/integrations/" + integrationID
	for _, a := range []answer{
		x.mutate(t, adminCookie, http.MethodPut, path, integrationBody, "If-Match", `"1"`),
		x.mutate(t, adminCookie, http.MethodDelete, path, ""),
		x.mutate(t, adminCookie, http.MethodPost, path+"/tokens", `{}`),
	} {
		if m := a.json(t); a.status != http.StatusConflict || m["code"] != "builtin_immutable" ||
			m["type"] != "https://muster-io.github.io/muster/problems/conflict" {
			t.Errorf("refusal = %d %s", a.status, a.body)
		}
	}
}

// TestAlertmanagerRoutesAPI is C-06.FR-18 and C-06.AC-3 at the API: each learned route with its interval and time to
// resolve by absence in seconds, null before learning, and the warning with its snippet.
func TestAlertmanagerRoutesAPI(t *testing.T) {
	x, _, _ := newIntegrationsAPI(t)
	fa := &fakeAlerts{}
	x.srv.alerts = fa
	five := 5*time.Minute + 400*time.Millisecond
	two := 2 * time.Hour
	fa.routes = []ingest.AlertmanagerRoute{
		{RoutePath: "{}", ResolveByAbsenceAfter: 25 * time.Hour, TruncatedGroupCount: 1},
		{RoutePath: `{}/{team="web"}`, LearnedRepeatInterval: &five, ResolveByAbsenceAfter: 3 * five},
		{RoutePath: `{}/{kind="info"}`, LearnedRepeatInterval: &two, ResolveByAbsenceAfter: 3 * two,
			LongIntervalWarning: true, RecommendedSnippet: "route:\n  repeat_interval: 10m\n"},
	}
	a := x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alertmanager-routes", "", "Cookie",
		viewerCookie)
	var page gen.AlertmanagerRouteList
	decodeInto(t, a, &page)
	if a.status != http.StatusOK || fa.integration != integrationID || len(page.Items) != 3 {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	unlearned, web, info := page.Items[0], page.Items[1], page.Items[2]
	if !unlearned.LearnedRepeatIntervalSeconds.IsNull() || unlearned.ResolveByAbsenceAfterSeconds.MustGet() != 90000 ||
		unlearned.TruncatedGroupCount != 1 || unlearned.LongIntervalWarning || !unlearned.RecommendedSnippet.IsNull() {
		t.Errorf("unlearned %s", a.body)
	}
	if web.LearnedRepeatIntervalSeconds.MustGet() != 300 || web.ResolveByAbsenceAfterSeconds.MustGet() != 901 {
		t.Errorf("web %s", a.body)
	}
	if info.LearnedRepeatIntervalSeconds.MustGet() != 7200 || !info.LongIntervalWarning ||
		info.RecommendedSnippet.MustGet() != "route:\n  repeat_interval: 10m\n" {
		t.Errorf("info %s", a.body)
	}
	fa.err = integrations.ErrNotFound
	if a := x.call(t, http.MethodGet, "/api/v1/integrations/"+integrationID+"/alertmanager-routes", "", "Cookie",
		viewerCookie); a.status != http.StatusNotFound {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
}
