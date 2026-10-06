// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/organization"
)

// fakeOrganization stands for the organization resource of internal/organization: it changes the TOTP policy and
// refuses a changed name, the one field it compares; err answers every call.
type fakeOrganization struct {
	org      organization.Organization
	inputs   []organization.Input
	versions []*int64
	actors   []audit.Actor
	err      error
}

func newFakeOrganization() *fakeOrganization {
	d := organization.Defaults()
	user := "hb"
	at := t0
	return &fakeOrganization{org: organization.Organization{
		PublicID: "RGAAAAAAAAAAAA", Name: d.Name, TimeZone: d.TimeZone, SeverityLabel: d.SeverityLabel,
		SeverityMapping: d.SeverityMapping, SeverityStyles: d.SeverityStyles, CriticalIsUrgent: d.CriticalIsUrgent,
		InstanceLabels: d.InstanceLabels, Retention: d.Retention, TOTPRequired: d.TOTPRequired,
		OIDCTokenGrace: d.OIDCTokenGrace, Version: 3,
		OutgoingHeartbeat: organization.OutgoingHeartbeat{
			URL:   organization.SecretState{Set: true, UpdatedAt: &at},
			Proxy: organization.Proxy{Enabled: true, Type: "http", Address: "proxy:3128", Username: &user},
		},
	}}
}

func (f *fakeOrganization) Get(context.Context) (organization.Organization, error) {
	return f.org, f.err
}

func (f *fakeOrganization) Update(_ context.Context, actor audit.Actor, _ audit.Transport, _ netip.Addr,
	version *int64, in organization.Input) (organization.Organization, error) {
	f.inputs, f.versions, f.actors = append(f.inputs, in), append(f.versions, version), append(f.actors, actor)
	switch {
	case f.err != nil:
		return organization.Organization{}, f.err
	case version != nil && *version != f.org.Version:
		return organization.Organization{}, organization.ErrVersionMismatch
	case in.Name != f.org.Name:
		return organization.Organization{}, &organization.UnsupportedError{Pointers: []string{"/name", "/time_zone"}}
	}
	f.org.TOTPRequired = in.TOTPRequired
	f.org.Version++
	return f.org, nil
}

func newOrganizationAPI(t *testing.T) (*testAPI, *fakeOrganization) {
	t.Helper()
	x := newTestAPI(t)
	f := newFakeOrganization()
	x.srv.organization = f
	return x, f
}

// updateBody is the body of an update from a read: without id and etag, with the outgoing heartbeat as written.
func updateBody(t *testing.T, read []byte, change func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(read, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "id")
	delete(m, "etag")
	m["outgoing_heartbeat"] = map[string]any{"proxy": map[string]any{"enabled": true, "type": "http",
		"address": "proxy:3128", "username": nil, "password": nil}, "url": "https://hb.example.org/ping"}
	if change != nil {
		change(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestGetOrganization is C-03.FR-20 and FR-21: every signed-in identity reads the organization resource with its
// ETag; the outgoing heartbeat URL and the proxy password show only whether they are set.
func TestGetOrganization(t *testing.T) {
	x, _ := newOrganizationAPI(t)
	for _, cookie := range []string{adminCookie, viewerCookie} {
		a := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", cookie)
		if a.status != http.StatusOK || a.header.Get("ETag") != `"3"` || a.json(t)["etag"] != `"3"` {
			t.Fatalf("%s = %d %s", cookie, a.status, a.body)
		}
		hb := a.json(t)["outgoing_heartbeat"].(map[string]any)
		if hb["url_status"].(map[string]any)["set"] != true || strings.Contains(string(a.body), "hb.example.org") {
			t.Errorf("outgoing heartbeat = %v", hb)
		}
		proxy := hb["proxy"].(map[string]any)
		if proxy["password_status"].(map[string]any)["set"] != false || proxy["username"] != "hb" ||
			proxy["address"] != "proxy:3128" {
			t.Errorf("proxy = %v", proxy)
		}
		if a.json(t)["totp_required"] != "nobody" || a.json(t)["oidc_token_grace_seconds"] != 604800.0 {
			t.Errorf("body = %s", a.body)
		}
	}
}

// TestUpdateOrganization is C-03.FR-20: an Admin changes the TOTP policy with If-Match; a stale ETag is 412, a
// missing one 428, a changed value of another field 422 with unsupported at each such field, a Viewer 403.
func TestUpdateOrganization(t *testing.T) {
	x, f := newOrganizationAPI(t)
	read := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie).body
	body := updateBody(t, read, func(m map[string]any) { m["totp_required"] = "everyone" })
	a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `"3"`)
	if a.status != http.StatusOK || a.json(t)["totp_required"] != "everyone" || a.header.Get("ETag") != `"4"` {
		t.Fatalf("update = %d %s", a.status, a.body)
	}
	in := f.inputs[0]
	if *f.versions[0] != 3 || f.actors[0].ID != 1 || in.TOTPRequired != organization.TOTPEveryone ||
		!in.HeartbeatURL.Given || in.HeartbeatURL.Value != "https://hb.example.org/ping" ||
		!in.HeartbeatProxy.UsernameSet || in.HeartbeatProxy.Username != nil || !in.HeartbeatProxy.Password.Null ||
		*in.HeartbeatProxy.Type != "http" || len(in.SeverityMapping) != 4 || len(in.SeverityStyles) != 3 ||
		in.Retention.AuditLogDays != 365 || in.OIDCTokenGraceSeconds != 604800 {
		t.Errorf("input = %+v", in)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `"3"`)
	if a.status != http.StatusPreconditionFailed {
		t.Errorf("a stale ETag = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body)
	if a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization",
		updateBody(t, read, func(m map[string]any) { m["name"] = "Other" }), "If-Match", "*")
	if a.status != http.StatusUnprocessableEntity {
		t.Fatalf("another field = %d %s", a.status, a.body)
	}
	var p struct {
		Errors []struct{ Pointer, Code string }
	}
	_ = json.Unmarshal(a.body, &p)
	if len(p.Errors) != 2 || p.Errors[0].Pointer != "/name" || p.Errors[0].Code != fieldUnsupported ||
		p.Errors[1].Pointer != "/time_zone" {
		t.Errorf("errors = %+v", p.Errors)
	}
	a = x.mutate(t, viewerCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `"4"`)
	if a.status != http.StatusForbidden {
		t.Errorf("a Viewer = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization",
		updateBody(t, read, func(m map[string]any) { m["totp_required"] = "sometimes" }), "If-Match", `"4"`)
	if a.status != http.StatusBadRequest {
		t.Errorf("an unknown policy = %d %s", a.status, a.body)
	}
	f.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `"4"`); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed update = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `W/"x"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("an If-Match that names no version = %d", a.status)
	}
}

// TestOrganizationHandlersWithoutIdentity: the update refuses a request without an identity or a body.
func TestOrganizationHandlersWithoutIdentity(t *testing.T) {
	x, _ := newOrganizationAPI(t)
	req := gen.UpdateOrganizationRequestObject{Params: gen.UpdateOrganizationParams{IfMatch: `"3"`}}
	if _, err := x.srv.UpdateOrganization(t.Context(), req); !errors.Is(err, errUnauthenticated) {
		t.Errorf("without an identity: %v", err)
	}
	id := &auth.Identity{Session: session(1, x.users.users[1], auth.StateActive), Transport: audit.TransportUI}
	if _, err := x.srv.UpdateOrganization(auth.WithIdentity(t.Context(), id), req); err == nil {
		t.Error("without a body: no error")
	}
	if got := secretInputOf(gen.UpdateOrganizationJSONRequestBody{}.OutgoingHeartbeat.Url); got.Given {
		t.Errorf("an omitted Secret = %+v", got)
	}
}
