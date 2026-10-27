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
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/organization"
)

// fakeOrganization stands for the organization resource of internal/organization: it changes the TOTP policy and the
// name, refuses an invalid colour and a retention order at their nested pointers, and keeps the outbound address
// policy; err answers every call.
type fakeOrganization struct {
	org          organization.Organization
	inputs       []organization.Input
	versions     []*int64
	actors       []audit.Actor
	policy       organization.OutboundPolicyResource
	policyInputs []organization.OutboundPolicyInput
	values       organization.SeverityValues
	err          error
}

func newFakeOrganization() *fakeOrganization {
	d := organization.Defaults()
	user := "hb"
	at := t0
	return &fakeOrganization{org: organization.Organization{
		PublicID: "RGAAAAAAAAAAAA", Name: d.Name, TimeZone: d.TimeZone, WorkdayStart: d.WorkdayStart,
		WeekStart: d.WeekStart, WorkingDays: d.WorkingDays, WorkdayEnd: d.WorkdayEnd, SeverityLabel: d.SeverityLabel,
		SeverityMapping: d.SeverityMapping, SeverityStyles: d.SeverityStyles, CriticalIsUrgent: d.CriticalIsUrgent,
		InstanceLabels: d.InstanceLabels, Retention: d.Retention, TOTPRequired: d.TOTPRequired,
		OIDCTokenGrace: d.OIDCTokenGrace, Version: 3,
		OutgoingHeartbeat: organization.OutgoingHeartbeat{
			URL:   organization.SecretState{Set: true, UpdatedAt: &at},
			Proxy: organization.Proxy{Enabled: true, Type: "http", Address: "proxy:3128", Username: &user},
		},
	}, policy: organization.OutboundPolicyResource{Mode: organization.OutboundStandard,
		Allowed: []string{"127.0.0.0/8"}, Denied: []string{}, AlwaysBlocked: []string{"link-local: 169.254.0.0/16"},
		Version: 2}}
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
	case len(in.SeverityStyles) > 1 && in.SeverityStyles[1].Color == "red":
		return organization.Organization{}, &organization.FieldError{Pointer: "/severity_styles/1/color",
			Code: organization.CodeInvalidFormat, Detail: "The colour is not a CSS hex colour such as #d32f2f."}
	case in.Retention.AlertGroupSummariesDays < in.Retention.AlertDetailsDays:
		return organization.Organization{}, &organization.FieldError{Pointer: "/retention/alert_group_summaries_days",
			Code: organization.CodeRetentionOrder, Detail: "Keep Alert Group summaries at least as long as alert details."}
	case strings.HasPrefix(string(in.HeartbeatURL.Value), "ftp:"):
		return organization.Organization{}, &organization.FieldError{Pointer: "/outgoing_heartbeat/url",
			Code: fieldInvalidFormat, Detail: "The URL is not an absolute http or https URL."}
	}
	f.org.TOTPRequired, f.org.Name = in.TOTPRequired, in.Name
	if in.WorkdayStart != nil {
		f.org.WorkdayStart = *in.WorkdayStart
	}
	if in.WeekStart != nil {
		f.org.WeekStart = *in.WeekStart
	}
	if in.HeartbeatURL.Given {
		f.org.OutgoingHeartbeat.URL.Set = !in.HeartbeatURL.Null
	}
	f.org.OutgoingHeartbeat.Proxy.Enabled = in.HeartbeatProxy.Enabled
	if in.HeartbeatProxy.Address != nil {
		f.org.OutgoingHeartbeat.Proxy.Address = *in.HeartbeatProxy.Address
	}
	f.org.Version++
	return f.org, nil
}

func (f *fakeOrganization) OutboundPolicy(context.Context) (organization.OutboundPolicyResource, error) {
	return f.policy, f.err
}

func (f *fakeOrganization) UpdateOutboundPolicy(_ context.Context, _ audit.Actor, _ audit.Transport, _ netip.Addr,
	version *int64, in organization.OutboundPolicyInput) (organization.OutboundPolicyResource, error) {
	f.policyInputs = append(f.policyInputs, in)
	switch {
	case f.err != nil:
		return organization.OutboundPolicyResource{}, f.err
	case version != nil && *version != f.policy.Version:
		return organization.OutboundPolicyResource{}, organization.ErrVersionMismatch
	case len(in.Allowed) > 0 && in.Allowed[0] == "bad entry":
		return organization.OutboundPolicyResource{}, &organization.FieldError{Pointer: "/allowed/0",
			Code: organization.CodeInvalidFormat, Detail: "The entry is not a network."}
	}
	f.policy.Mode, f.policy.Allowed, f.policy.Denied = in.Mode, in.Allowed, in.Denied
	f.policy.Version++
	return f.policy, nil
}

func (f *fakeOrganization) SeverityValues(context.Context) (organization.SeverityValues, error) {
	return f.values, f.err
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
		if a.json(t)["totp_required"] != "nobody" || a.json(t)["oidc_token_grace_seconds"] != 604800.0 ||
			a.json(t)["workday_start"] != "09:00" || a.json(t)["week_start"] != "monday" {
			t.Errorf("body = %s", a.body)
		}
	}
}

// TestUpdateOrganization is C-03.FR-20 and C-20.FR-1, FR-8: an Admin changes the settings with If-Match; a stale ETag
// is 412, a missing one 428, an invalid field 422 at its full nested pointer, a Viewer 403.
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
	if a.status != http.StatusOK || a.json(t)["name"] != "Other" || strings.Contains(string(a.body), "unsupported") {
		t.Fatalf("another field = %d %s", a.status, a.body)
	}
	for _, c := range []struct {
		change        func(map[string]any)
		pointer, code string
	}{
		{func(m map[string]any) { m["severity_styles"].([]any)[1].(map[string]any)["color"] = "red" },
			"/severity_styles/1/color", fieldInvalidFormat},
		{func(m map[string]any) {
			m["retention"].(map[string]any)["alert_group_summaries_days"] = 30
			m["retention"].(map[string]any)["alert_details_days"] = 90
		}, "/retention/alert_group_summaries_days", "retention_order"},
	} {
		a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read, c.change),
			"If-Match", "*")
		var p struct {
			Errors []struct{ Pointer, Code string }
		}
		_ = json.Unmarshal(a.body, &p)
		if a.status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Pointer != c.pointer ||
			p.Errors[0].Code != c.code {
			t.Errorf("%s = %d %s", c.pointer, a.status, a.body)
		}
	}
	// The limits of the spec answer 400 before the service.
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read, func(m map[string]any) {
		m["retention"].(map[string]any)["audit_log_days"] = 36501
	}), "If-Match", "*")
	if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), "/retention/audit_log_days") {
		t.Errorf("a retention past the limit = %d %s", a.status, a.body)
	}
	a = x.mutate(t, viewerCookie, http.MethodPut, "/api/v1/organization", body, "If-Match", `"5"`)
	if a.status != http.StatusForbidden {
		t.Errorf("a Viewer = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization",
		updateBody(t, read, func(m map[string]any) { m["totp_required"] = "sometimes" }), "If-Match", `"5"`)
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

// TestCriticalIsUrgentAPI is C-26.FR-10 and C-26.AC-8 at the API: getOrganization reads critical_is_urgent true even
// where the stored resource says otherwise, and an update that sends false, or leaves the field out, is accepted
// without passing it on.
func TestCriticalIsUrgentAPI(t *testing.T) {
	x, f := newOrganizationAPI(t)
	f.org.CriticalIsUrgent = false
	a := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || a.json(t)["critical_is_urgent"] != true {
		t.Fatalf("read = %d %s", a.status, a.body)
	}
	for i, change := range []func(map[string]any){
		func(m map[string]any) { m["critical_is_urgent"] = false },
		func(m map[string]any) { delete(m, "critical_is_urgent") },
	} {
		a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, a.body, change),
			"If-Match", "*")
		if a.status != http.StatusOK || a.json(t)["critical_is_urgent"] != true || len(f.inputs) != i+1 ||
			f.inputs[i].CriticalIsUrgent {
			t.Errorf("update %d = %d %s", i, a.status, a.body)
		}
	}
}

// TestUpdateOrganizationSnoozePresetSettings is C-20.FR-9 and C-20.AC-7 on the API: an Admin sets the start of the
// working day and the first day of the week, which the answer carries; an update that omits both keeps them; a value
// outside the pattern or the days of the week is refused by the request validation with 400 invalid_format at the
// field, before the service.
func TestUpdateOrganizationSnoozePresetSettings(t *testing.T) {
	x, f := newOrganizationAPI(t)
	read := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie).body
	a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read, func(m map[string]any) {
		m["workday_start"], m["week_start"] = "08:30", "sunday"
	}), "If-Match", "*")
	if a.status != http.StatusOK || a.json(t)["workday_start"] != "08:30" || a.json(t)["week_start"] != "sunday" {
		t.Fatalf("08:30 with sunday = %d %s", a.status, a.body)
	}
	if in := f.inputs[0]; in.WorkdayStart == nil || *in.WorkdayStart != "08:30" || in.WeekStart == nil ||
		*in.WeekStart != "sunday" {
		t.Errorf("input %+v", in)
	}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read, func(m map[string]any) {
		delete(m, "workday_start")
		delete(m, "week_start")
	}), "If-Match", "*")
	if a.status != http.StatusOK || a.json(t)["workday_start"] != "08:30" || a.json(t)["week_start"] != "sunday" {
		t.Fatalf("an update that omits both = %d %s", a.status, a.body)
	}
	if in := f.inputs[1]; in.WorkdayStart != nil || in.WeekStart != nil {
		t.Errorf("an omitted field is not kept: %+v", in)
	}
	for _, c := range []struct{ field, value, pointer string }{
		{"workday_start", "24:00", "/workday_start"},
		{"workday_start", "9:00", "/workday_start"},
		{"week_start", "weekend", "/week_start"},
	} {
		a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read,
			func(m map[string]any) { m[c.field] = c.value }), "If-Match", "*")
		var p struct {
			Errors []struct{ Pointer, Code string }
		}
		_ = json.Unmarshal(a.body, &p)
		if a.status != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Pointer != c.pointer ||
			p.Errors[0].Code != fieldInvalidFormat || len(f.inputs) != 2 {
			t.Errorf("%s %s = %d %s", c.field, c.value, a.status, a.body)
		}
	}
	// The service's own check of the same rules answers 422 at the field.
	f.err = &organization.FieldError{Pointer: "/week_start", Code: organization.CodeInvalidFormat,
		Detail: "The first day of the week is not a day from monday to sunday."}
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", updateBody(t, read, nil), "If-Match", "*")
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/week_start"`) {
		t.Errorf("the service's refusal = %d %s", a.status, a.body)
	}
}

// TestUpdateOrganizationOutgoingHeartbeat covers C-19.FR-7 on the API: an Admin sets the write-only URL of the
// outgoing heartbeat, read back only as set, clears it with null and sets its proxy; an invalid URL is 422 at its
// pointer; no answer carries the URL.
func TestUpdateOrganizationOutgoingHeartbeat(t *testing.T) {
	x, f := newOrganizationAPI(t)
	f.org.OutgoingHeartbeat = organization.OutgoingHeartbeat{}
	put := func(change func(hb map[string]any)) answer {
		t.Helper()
		read := x.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie)
		body := updateBody(t, read.body, func(m map[string]any) {
			change(m["outgoing_heartbeat"].(map[string]any))
		})
		return x.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization", body, "If-Match",
			read.header.Get("ETag"))
	}
	a := put(func(hb map[string]any) {
		hb["url"] = "https://hb.example.org/ping/secret-token"
		hb["proxy"] = map[string]any{"enabled": false}
	})
	if a.status != http.StatusOK || a.json(t)["outgoing_heartbeat"].(map[string]any)["url_status"].(map[string]any)["set"] != true ||
		strings.Contains(string(a.body), "secret-token") {
		t.Fatalf("set = %d %s", a.status, a.body)
	}
	if in := f.inputs[len(f.inputs)-1]; in.HeartbeatURL.Value != "https://hb.example.org/ping/secret-token" ||
		in.HeartbeatProxy.Enabled {
		t.Errorf("input %+v", in)
	}
	a = put(func(hb map[string]any) {
		hb["url"] = nil
		hb["proxy"] = map[string]any{"enabled": true, "type": "http", "address": "127.0.0.1:18091"}
	})
	hb := a.json(t)["outgoing_heartbeat"].(map[string]any)
	if a.status != http.StatusOK || hb["url_status"].(map[string]any)["set"] != false ||
		hb["proxy"].(map[string]any)["address"] != "127.0.0.1:18091" {
		t.Fatalf("clear = %d %s", a.status, a.body)
	}
	if in := f.inputs[len(f.inputs)-1]; !in.HeartbeatURL.Null || !in.HeartbeatProxy.Enabled {
		t.Errorf("input %+v", in)
	}
	a = put(func(hb map[string]any) { hb["url"] = "ftp://hb.example.org/x" })
	var p struct {
		Errors []struct{ Pointer, Code string }
	}
	_ = json.Unmarshal(a.body, &p)
	if a.status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Pointer !=
		"/outgoing_heartbeat/url" || p.Errors[0].Code != fieldInvalidFormat || strings.Contains(string(a.body), "ftp:") {
		t.Errorf("an ftp URL = %d %s", a.status, a.body)
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

// TestOutboundPolicyAPI is C-20.FR-3 on the API: an Admin reads the policy with its ETag and the rules that always
// apply, and replaces it with If-Match; always_blocked in the body is ignored; a stale ETag is 412, a missing one 428,
// an invalid entry 422 at its pointer; a Viewer is 403 on both.
func TestOutboundPolicyAPI(t *testing.T) {
	x, f := newOrganizationAPI(t)
	const path = "/api/v1/organization/outbound-policy"
	a := x.call(t, http.MethodGet, path, "", "Cookie", adminCookie)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"2"` || a.json(t)["etag"] != `"2"` ||
		a.json(t)["policy"] != "standard" || len(a.json(t)["always_blocked"].([]any)) != 1 {
		t.Fatalf("read = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, path, "", "Cookie", viewerCookie); a.status != http.StatusForbidden {
		t.Errorf("a Viewer reads = %d", a.status)
	}
	body := `{"policy":"strict","allowed":["10.0.0.0/8"],"denied":[],"always_blocked":["nothing"]}`
	a = x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `"2"`)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"3"` || a.json(t)["policy"] != "strict" ||
		a.json(t)["always_blocked"].([]any)[0] != "link-local: 169.254.0.0/16" {
		t.Fatalf("update = %d %s", a.status, a.body)
	}
	if in := f.policyInputs[0]; in.Mode != organization.OutboundStrict || len(in.Allowed) != 1 || in.Denied == nil {
		t.Errorf("input %+v", in)
	}
	for _, c := range []struct {
		headers []string
		body    string
		status  int
	}{
		{[]string{"If-Match", `"2"`}, body, http.StatusPreconditionFailed},
		{nil, body, http.StatusPreconditionRequired},
		{[]string{"If-Match", "*"}, `{"policy":"open","allowed":[],"denied":[]}`, http.StatusBadRequest},
		{[]string{"If-Match", "*"}, `{"policy":"standard","allowed":["bad entry"],"denied":[]}`,
			http.StatusUnprocessableEntity},
	} {
		a := x.mutate(t, adminCookie, http.MethodPut, path, c.body, c.headers...)
		if a.status != c.status {
			t.Errorf("%s %v = %d %s", c.body, c.headers, a.status, a.body)
		}
		if c.status == http.StatusUnprocessableEntity && !strings.Contains(string(a.body), `"pointer":"/allowed/0"`) {
			t.Errorf("the pointer of an invalid entry: %s", a.body)
		}
	}
	if a := x.mutate(t, viewerCookie, http.MethodPut, path, body, "If-Match", "*"); a.status != http.StatusForbidden {
		t.Errorf("a Viewer updates = %d", a.status)
	}
	if a := x.call(t, http.MethodPut, path, body, "Cookie", adminCookie, "If-Match", "*"); a.status !=
		http.StatusForbidden {
		t.Errorf("without the CSRF token = %d", a.status)
	}
	f.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, path, "", "Cookie", adminCookie); a.status != http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", "*"); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed update = %d", a.status)
	}
	if a := x.mutate(t, adminCookie, http.MethodPut, path, body, "If-Match", `W/"x"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("an If-Match that names no version = %d", a.status)
	}
	req := gen.UpdateOutboundPolicyRequestObject{Params: gen.UpdateOutboundPolicyParams{IfMatch: "*"}}
	if _, err := x.srv.UpdateOutboundPolicy(t.Context(), req); !errors.Is(err, errUnauthenticated) {
		t.Errorf("without an identity: %v", err)
	}
	id := &auth.Identity{Session: session(1, x.users.users[1], auth.StateActive), Transport: audit.TransportUI}
	if _, err := x.srv.UpdateOutboundPolicy(auth.WithIdentity(t.Context(), id), req); err == nil {
		t.Error("without a body: no error")
	}
}

// TestListSeverityValues is C-20.AC-4 on the API: the values seen with their counts, levels and whether they are
// mapped, and the window; Admins only.
func TestListSeverityValues(t *testing.T) {
	x, f := newOrganizationAPI(t)
	f.values = organization.SeverityValues{WindowDays: 14, Items: []organization.SeverityValue{
		{Value: "P5", Count: 2, Level: organization.SeverityWarning},
		{Value: "critical", Count: 1, Level: organization.SeverityCritical, Mapped: true},
	}}
	a := x.call(t, http.MethodGet, "/api/v1/organization/severity-values", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || string(a.body) != `{"items":[{"count":2,"level":"warning","mapped":false,`+
		`"value":"P5"},{"count":1,"level":"critical","mapped":true,"value":"critical"}],"window_days":14}`+"\n" {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/organization/severity-values", "", "Cookie", viewerCookie); a.status !=
		http.StatusForbidden {
		t.Errorf("a Viewer = %d", a.status)
	}
	f.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, "/api/v1/organization/severity-values", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
}

// fakeKeyring is the Keyring status of the API tests.
type fakeKeyring struct {
	status keyring.Status
	err    error
}

func (f fakeKeyring) Status(context.Context) (keyring.Status, error) { return f.status, f.err }

// TestGetKeyring is C-20.FR-6: with organization:read, the Keyring lists every key with the live replicas that hold
// and lack it, what depends on it and its state, in the order of the status, valid against the specification; a
// Viewer is 403, and a failed read is a 500 problem.
func TestGetKeyring(t *testing.T) {
	x := newTestAPI(t)
	x.srv.keyring = fakeKeyring{status: keyring.Status{ActiveKeyID: "k-1", Keys: []keyring.KeyStatus{
		{ID: "k-1", Active: true, HeldBy: []string{"r-a", "r-b"}, MissingOn: []string{}, Needed: true,
			State: keyring.KeyActive},
		{ID: "k-0", HeldBy: []string{"r-a"}, MissingOn: []string{"r-b"}, Removable: true, State: keyring.KeyRemovable},
		{ID: "k-2", HeldBy: []string{"r-a", "r-b"}, MissingOn: []string{}, Needed: true, State: keyring.KeyNeeded},
		{ID: "k-3", HeldBy: []string{}, MissingOn: []string{"r-a", "r-b"}, State: keyring.KeyNotActiveYet},
	}}}
	a := x.call(t, http.MethodGet, "/api/v1/organization/keyring", "", "Cookie", adminCookie)
	want := `{"keys":[` +
		`{"active":true,"held_by":["r-a","r-b"],"id":"k-1","missing_on":[],"needed":true,"removable":false,"state":"active"},` +
		`{"active":false,"held_by":["r-a"],"id":"k-0","missing_on":["r-b"],"needed":false,"removable":true,"state":"removable"},` +
		`{"active":false,"held_by":["r-a","r-b"],"id":"k-2","missing_on":[],"needed":true,"removable":false,"state":"needed"},` +
		`{"active":false,"held_by":[],"id":"k-3","missing_on":["r-a","r-b"],"needed":false,"removable":false,"state":"not_active_yet"}]}`
	if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) != want {
		t.Errorf("GET = %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/organization/keyring", "", "Cookie", viewerCookie); a.status !=
		http.StatusForbidden {
		t.Errorf("a viewer = %d", a.status)
	}
	x.srv.keyring = fakeKeyring{err: errors.New("database down")}
	if a := x.call(t, http.MethodGet, "/api/v1/organization/keyring", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed read = %d %s", a.status, a.body)
	}
}
