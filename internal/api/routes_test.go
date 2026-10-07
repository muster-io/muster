// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/routing"
)

const (
	routeID        = "RTAAAAAAAAAAAA"
	defaultRouteID = "RTDDDDDDDDDDDD"
)

// fakeRoutes stands for internal/routing: the Route payments before the Default route, at list version 4.
type fakeRoutes struct {
	list     routing.List
	inputs   []routing.Input
	versions []*int64
	reorders [][]string
	by       []routing.Requester
	err      error

	previews     []routing.PreviewRequest
	users        []*int64
	accepted     [][]string
	dismissed    []int64
	suggestionOK bool
}

func newFakeRoutes() *fakeRoutes {
	p := routing.Profiles()[0]
	return &fakeRoutes{list: routing.List{Version: 4, Routes: []routing.Route{
		{ID: 2, PublicID: routeID, Name: "payments", Description: "Payments", Position: 0, Urgent: true,
			Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: "payments"}}, GroupKey: []string{"alertname"},
			Policy: p.Policy, CreatedAt: t0, Version: 3},
		{ID: 1, PublicID: defaultRouteID, Name: "Default", Position: 1, IsDefault: true, GroupKey: p.GroupKey,
			Policy: p.Policy, CreatedAt: t0, Version: 1},
	}}}
}

func (f *fakeRoutes) List(context.Context) (routing.List, error) { return f.list, f.err }

func (f *fakeRoutes) Get(_ context.Context, id string) (routing.Route, error) {
	for _, r := range f.list.Routes {
		if r.PublicID == id {
			return r, f.err
		}
	}
	return routing.Route{}, routing.ErrNotFound
}

func (f *fakeRoutes) Create(_ context.Context, r routing.Requester, in routing.Input) (routing.Route, error) {
	f.by, f.inputs = append(f.by, r), append(f.inputs, in)
	switch {
	case in.Name == "taken":
		return routing.Route{}, routing.ErrNameTaken
	case len(in.Matchers) > 0 && in.Matchers[0].Value == "api-(":
		return routing.Route{}, &routing.FieldError{Pointer: "/matchers/0/value", Code: routing.CodeInvalidRegex,
			Detail: "error parsing regexp: missing closing ): `api-(`"}
	}
	out := f.list.Routes[0]
	out.Name, out.Matchers, out.Version = in.Name, nil, 1
	return out, f.err
}

func (f *fakeRoutes) Update(_ context.Context, r routing.Requester, id string, version *int64, in routing.Input) (
	routing.Route, error) {
	f.by, f.inputs, f.versions = append(f.by, r), append(f.inputs, in), append(f.versions, version)
	rt, err := f.Get(context.Background(), id)
	if err != nil {
		return routing.Route{}, err
	}
	if version != nil && *version != rt.Version {
		return routing.Route{}, routing.ErrVersionMismatch
	}
	rt.Name, rt.Version = in.Name, rt.Version+1
	return rt, f.err
}

func (f *fakeRoutes) Delete(_ context.Context, r routing.Requester, id string, version *int64) error {
	f.by, f.versions = append(f.by, r), append(f.versions, version)
	if id == defaultRouteID {
		return routing.ErrDefaultImmutable
	}
	if _, err := f.Get(context.Background(), id); err != nil {
		return err
	}
	return f.err
}

func (f *fakeRoutes) Reorder(_ context.Context, r routing.Requester, version *int64, ids []string) (routing.List,
	error) {
	f.by, f.versions, f.reorders = append(f.by, r), append(f.versions, version), append(f.reorders, ids)
	switch {
	case version != nil && *version != f.list.Version:
		return routing.List{}, routing.ErrVersionMismatch
	case slices.Contains(ids, defaultRouteID):
		return routing.List{}, routing.ErrDefaultImmutable
	case len(ids) != 1:
		return routing.List{}, &routing.FieldError{Pointer: "/route_ids", Code: routing.CodeRouteSetMismatch,
			Detail: "The Routes differ from the current set."}
	}
	out := f.list
	out.Version++
	return out, f.err
}

func (f *fakeRoutes) Preview(_ context.Context, req routing.PreviewRequest) (routing.Preview, error) {
	f.previews = append(f.previews, req)
	switch {
	case req.RouteID == "" && req.Matchers == nil:
		return routing.Preview{}, &routing.FieldError{Code: routing.CodeOneOfRequired, Detail: "Give one."}
	case req.RouteID == "RTZZZZZZZZZZZZ":
		return routing.Preview{}, routing.ErrNotFound
	}
	side := routing.PreviewSide{AlertGroupCount: 2, Examples: []routing.PreviewExample{
		{GroupKeyValues: map[string]string{"cluster": ""}, AlertCount: 2},
		{GroupKeyValues: map[string]string{"cluster": "a"}, AlertCount: 2}}}
	out := routing.Preview{PeriodSeconds: 86400, Proposed: side, Truncated: true}
	if req.RouteID != "" {
		out.Current = &routing.PreviewSide{AlertGroupCount: 1, Examples: []routing.PreviewExample{
			{GroupKeyValues: map[string]string{}, AlertCount: 4}}}
	}
	return out, f.err
}

func (f *fakeRoutes) Suggestions(_ context.Context, user *int64) ([]routing.Suggestion, error) {
	f.users = append(f.users, user)
	if !f.suggestionOK {
		return nil, f.err
	}
	return []routing.Suggestion{{ID: routing.SuggestionHeartbeatLost, Route: routing.Input{
		Name: routing.HeartbeatLostRouteName, Urgent: true, GroupKey: []string{"alertname", "integration"},
		Matchers: []routing.Matcher{{Label: "alertname", Op: "=", Value: "MusterHeartbeatLost"}},
		Policy:   routing.Profiles()[0].Policy}}}, f.err
}

func (f *fakeRoutes) AcceptSuggestion(_ context.Context, r routing.Requester, id string, destinations []string) (
	routing.Route, error) {
	f.by, f.accepted = append(f.by, r), append(f.accepted, append([]string{id}, destinations...))
	if !f.suggestionOK || id != routing.SuggestionHeartbeatLost {
		return routing.Route{}, routing.ErrSuggestionObsolete
	}
	out := f.list.Routes[0]
	out.Name, out.Version = routing.HeartbeatLostRouteName, 1
	return out, f.err
}

func (f *fakeRoutes) DismissSuggestion(_ context.Context, user int64, id string) error {
	f.dismissed = append(f.dismissed, user)
	switch {
	case id != routing.SuggestionHeartbeatLost:
		return routing.ErrSuggestionObsolete
	case !f.suggestionOK:
		return routing.ErrSuggestionObsolete
	}
	return f.err
}

// The tokens of the Route tests: one with routes:read and routes:write, as an Admin holds them, and one with
// routes:read alone, as every Role holds it.
const (
	routesWriter  = "mstr_pat_routes_write"
	routesReader  = "mstr_pat_routes_read"
	routesService = "mstr_sat_routes"
)

func newRoutesAPI(t *testing.T) (*testAPI, *fakeRoutes) {
	t.Helper()
	x, ft, _, _ := newTokensAPI(t)
	owner := ft.idents[fullToken].Session
	ft.idents[routesWriter] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"routes:read",
		"routes:write"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 31, Name: "routes"}}
	ft.idents[routesReader] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"routes:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 32, Name: "routes-read"}}
	ft.idents[routesService] = &auth.Identity{Permissions: []auth.Permission{"routes:read", "routes:write"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 33, Name: "ci",
			ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "admin"}}}
	fr := newFakeRoutes()
	x.srv.routes = fr
	return x, fr
}

// as sends a request with the bearer token.
func (x *testAPI) as(t *testing.T, token, method, path, body string, headers ...string) answer {
	t.Helper()
	return x.call(t, method, path, body, append(bearer(token), headers...)...)
}

const routeBody = `{"name":"payments","matchers":[{"label":"team","op":"=","value":"payments"},
	{"label":"pod","op":"=~","value":"api-.*"}],"urgent":true,"group_key":["alertname"],"destination_ids":[],
	"policy":{"reopen_window_seconds":900,"grace_period_seconds":900,"urgent_rise_removes_ack":true,
	"snooze_durations_seconds":[3600,14400],"thread_batching_window_seconds":60,"storm_threshold":20,"language":"ru",
	"templates":{"root_message":null},"ack_timeout":{"enabled":true,"first_interval_seconds":900},
	"reminders":{"enabled":false,"first_interval_seconds":14400,"cap_seconds":86400},"auto_unacknowledge":true}}`

// TestRoutesAPI is C-08.FR-1, FR-9 and FR-10 at the API with routes:read and routes:write: the Route with its fields
// and the defaults of later capabilities, ETag and Location on creation, If-Match on update (428 without, 412 when
// stale), the Default route refused as default-route-immutable, and the refusals of the domain as problems.
func TestRoutesAPI(t *testing.T) {
	x, fr := newRoutesAPI(t)
	a := x.as(t, routesReader, http.MethodGet, "/api/v1/routes", "")
	var list gen.RouteList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || len(list.Items) != 2 {
		t.Fatalf("list = %d %v %s", a.status, a.header, a.body)
	}
	p, d := list.Items[0], list.Items[1]
	if p.Id != routeID || p.Position != 0 || p.IsDefault || !p.Urgent || *p.Etag != `"3"` || *p.Description != "Payments" ||
		len(p.Matchers) != 1 || p.Matchers[0].Op != gen.MatcherOp("=") || len(p.Destinations) != 0 ||
		len(p.DestinationIds) != 0 || p.StormActive || p.Storm != nil || p.TemplateError != nil ||
		p.OpenAlertGroupCount != 0 || p.Policy.Language != gen.LanguageEn || !p.Policy.Templates.RootMessage.IsNull() ||
		!slices.Equal(p.Policy.SnoozeDurationsSeconds, []int{3600, 14400, 86400}) || !d.IsDefault || d.Position != 1 ||
		len(d.Matchers) != 0 {
		t.Errorf("list %s", a.body)
	}
	if strings.Contains(string(a.body), `"template_error"`) {
		t.Errorf("template_error is present: %s", a.body)
	}

	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", routeBody)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` || a.header.Get("Location") != "/api/v1/routes/"+routeID ||
		a.json(t)["name"] != "payments" {
		t.Errorf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := fr.inputs[0]
	if in.Name != "payments" || len(in.Matchers) != 2 || in.Matchers[1] != (routing.Matcher{Label: "pod", Op: "=~",
		Value: "api-.*"}) || !in.Urgent || !slices.Equal(in.GroupKey, []string{"alertname"}) || len(in.DestinationIDs) != 0 ||
		in.Description != nil || in.Policy.Language != "ru" || in.Policy.Templates != (routing.Templates{}) ||
		!slices.Equal(in.Policy.SnoozeDurationsSeconds, []int64{3600, 14400}) || in.Policy.Reminders.Enabled ||
		in.Policy.Reminders.CapSeconds != 86400 || !in.Policy.AutoUnacknowledge || !in.Policy.AckTimeout.Enabled ||
		in.Policy.StormThreshold != 20 || fr.by[0].Actor.Kind != audit.ActorUser || fr.by[0].Transport != audit.TransportAPI ||
		fr.by[0].Actor.TokenName != "routes" {
		t.Errorf("input %+v by %+v", in, fr.by[0])
	}
	withTemplate := strings.Replace(routeBody, `"root_message":null`, `"root_message":"{{ .Title }}","line":null`, 1)
	if a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", withTemplate); a.status != http.StatusCreated ||
		fr.inputs[1].Policy.Templates.RootMessage == nil || *fr.inputs[1].Policy.Templates.RootMessage != "{{ .Title }}" ||
		fr.inputs[1].Policy.Templates.Line != nil {
		t.Errorf("template = %d %+v", a.status, fr.inputs[1].Policy.Templates)
	}
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", strings.Replace(routeBody, `"name":"payments"`,
		`"name":"taken"`, 1))
	if a.status != http.StatusConflict || a.code(t) != "name_taken" {
		t.Errorf("taken = %d %s", a.status, a.body)
	}
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", strings.Replace(routeBody, `"value":"payments"`,
		`"value":"api-("`, 1))
	errs, _ := a.json(t)["errors"].([]any)
	first, _ := errs[0].(map[string]any)
	if a.status != http.StatusUnprocessableEntity || first["pointer"] != "/matchers/0/value" ||
		first["code"] != "invalid_regex" {
		t.Errorf("invalid regex = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", strings.Replace(routeBody, `"op":"=~"`,
		`"op":"=="`, 1)); a.status != http.StatusBadRequest {
		t.Errorf("an operator outside the enum = %d", a.status)
	}
	if a = x.as(t, routesReader, http.MethodPost, "/api/v1/routes", routeBody); a.status != http.StatusForbidden {
		t.Errorf("create by a viewer = %d", a.status)
	}

	path := "/api/v1/routes/" + routeID
	if a = x.as(t, routesReader, http.MethodGet, path, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"3"` {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesReader, http.MethodGet, "/api/v1/routes/RTZZZZZZZZZZZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("get unknown = %d", a.status)
	}
	if a = x.as(t, routesWriter, http.MethodPut, path, routeBody); a.status != http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, routesWriter, http.MethodPut, path, routeBody, "If-Match", `"2"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("update with a stale If-Match = %d", a.status)
	}
	if a = x.as(t, routesWriter, http.MethodPut, path, routeBody, "If-Match", `"3"`); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"4"` {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPut, path, routeBody, "If-Match", "*"); a.status != http.StatusOK ||
		fr.versions[len(fr.versions)-1] != nil {
		t.Errorf("update with If-Match * = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesReader, http.MethodPut, path, routeBody, "If-Match", `"3"`); a.status != http.StatusForbidden {
		t.Errorf("update by a viewer = %d", a.status)
	}
	a = x.as(t, routesWriter, http.MethodDelete, "/api/v1/routes/"+defaultRouteID, "")
	if a.status != http.StatusConflict || a.json(t)["type"] != problemBase+"default-route-immutable" {
		t.Errorf("delete the default route = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodDelete, path, "", "If-Match", `"3"`); a.status != http.StatusNoContent ||
		*fr.versions[len(fr.versions)-1] != 3 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodDelete, path, ""); a.status != http.StatusNoContent ||
		fr.versions[len(fr.versions)-1] != nil {
		t.Errorf("delete without If-Match = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodDelete, "/api/v1/routes/RTZZZZZZZZZZZZ", ""); a.status != http.StatusNotFound {
		t.Errorf("delete unknown = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/routes", "", bearer(readOnlyToken)...); a.status != http.StatusForbidden {
		t.Errorf("list without routes:read = %d", a.status)
	}
}

// TestReorderRoutesAPI is C-08.FR-10, C-08.AC-6, AC-7 and AC-11 at the API: the list ETag in If-Match (428 without,
// 412 when stale), 409 default-route-immutable for a list with the Default route, 422 route_set_mismatch, and the
// new order with the next list ETag.
func TestReorderRoutesAPI(t *testing.T) {
	x, fr := newRoutesAPI(t)
	body := `{"route_ids":["` + routeID + `"]}`
	if a := x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", body); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d", a.status)
	}
	if a := x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", body, "If-Match", `"3"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d", a.status)
	}
	a := x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", `{"route_ids":["`+routeID+`","`+
		defaultRouteID+`"]}`, "If-Match", `"4"`)
	if a.status != http.StatusConflict || a.json(t)["type"] != problemBase+"default-route-immutable" {
		t.Errorf("default = %d %s", a.status, a.body)
	}
	a = x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", `{"route_ids":[]}`, "If-Match", `"4"`)
	errs, _ := a.json(t)["errors"].([]any)
	first, _ := errs[0].(map[string]any)
	if a.status != http.StatusUnprocessableEntity || first["code"] != "route_set_mismatch" ||
		first["pointer"] != "/route_ids" {
		t.Errorf("mismatch = %d %s", a.status, a.body)
	}
	a = x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", body, "If-Match", `"4"`)
	var list gen.RouteList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"5"` || len(list.Items) != 2 ||
		!slices.Equal(fr.reorders[len(fr.reorders)-1], []string{routeID}) {
		t.Errorf("reorder = %d %v %s", a.status, a.header, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPut, "/api/v1/route-order", body, "If-Match", "*"); a.status != http.StatusOK ||
		fr.versions[len(fr.versions)-1] != nil {
		t.Errorf("reorder with If-Match * = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesReader, http.MethodPut, "/api/v1/route-order", body, "If-Match", `"4"`); a.status !=
		http.StatusForbidden {
		t.Errorf("reorder by a viewer = %d", a.status)
	}
}

// TestListRouteProfilesAPI is C-08.FR-7 and C-08.AC-11: On-call and Informational with the values of defaults.md,
// for routes:read.
func TestListRouteProfilesAPI(t *testing.T) {
	x, _ := newRoutesAPI(t)
	a := x.as(t, routesReader, http.MethodGet, "/api/v1/route-profiles", "")
	var list gen.RouteProfileList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 {
		t.Fatalf("profiles = %d %s", a.status, a.body)
	}
	on, info := list.Items[0], list.Items[1]
	if on.Id != gen.OnCall || on.Name != "On-call" || !slices.Equal(on.Policy.SnoozeDurationsSeconds, []int{3600, 14400, 86400}) ||
		!on.Policy.AckTimeout.Enabled || on.Policy.AckTimeout.FirstIntervalSeconds != 900 || !on.Policy.Reminders.Enabled ||
		on.Policy.Reminders.FirstIntervalSeconds != 14400 || on.Policy.Reminders.CapSeconds != 86400 ||
		!slices.Equal(on.GroupKey, []string{"alertname", "severity", "cluster"}) || on.Urgent {
		t.Errorf("on-call %+v", on)
	}
	if info.Id != gen.Informational || info.Name != "Informational" ||
		!slices.Equal(info.Policy.SnoozeDurationsSeconds, []int{86400, 259200, 604800}) || info.Policy.AckTimeout.Enabled ||
		info.Policy.Reminders.Enabled {
		t.Errorf("informational %+v", info)
	}
	if !strings.Contains(string(a.body), `"templates":{"ack_timeout_notice":null,"line":null,"root_message":null}`) {
		t.Errorf("templates are not null: %s", a.body)
	}
}

// TestIntegrationAlertRoute is C-06.FR-19 and C-08.FR-13: the Alerts view shows the Route and the Severity level of
// each routed Alert, and the value as received when it has no mapping.
func TestIntegrationAlertRoute(t *testing.T) {
	level, raw := "warning", "P5"
	got := integrationAlertOf(ingest.ViewAlert{Route: &ingest.RouteRef{PublicID: routeID, Name: "payments"},
		SeverityLevel: &level, SeverityRaw: &raw})
	if got.Route == nil || got.Route.Id != routeID || got.Route.Name != "payments" || got.SeverityLevel == nil ||
		*got.SeverityLevel != gen.SeverityLevel("warning") || got.SeverityRaw.MustGet() != "P5" {
		t.Errorf("routed %+v", got)
	}
	level = "info"
	got = integrationAlertOf(ingest.ViewAlert{Route: &ingest.RouteRef{PublicID: routeID, Name: "payments"},
		SeverityLevel: &level})
	if !got.SeverityRaw.IsNull() || *got.SeverityLevel != gen.SeverityLevel("info") {
		t.Errorf("mapped %+v", got)
	}
	got = integrationAlertOf(ingest.ViewAlert{})
	if got.Route != nil || got.SeverityLevel != nil || got.SeverityRaw.IsSpecified() {
		t.Errorf("before routing %+v", got)
	}
}

// TestPreviewGroupKeyAPI is C-08.FR-5, C-08.AC-2 and C-08.AC-12 at the API with routes:write: the request as the
// domain reads it, the preview with current absent for an unsaved Route, the empty value of a missing label, and the
// refusals as problems.
func TestPreviewGroupKeyAPI(t *testing.T) {
	x, fr := newRoutesAPI(t)
	a := x.as(t, routesWriter, http.MethodPost, "/api/v1/group-key-previews",
		`{"matchers":[{"label":"alertname","op":"=","value":"Disk"}],"proposed_group_key":["alertname","cluster"]}`)
	if a.status != http.StatusOK || strings.Contains(string(a.body), `"current"`) ||
		!strings.Contains(string(a.body), `"examples":[{"alert_count":2,"group_key_values":{"cluster":""}}`) ||
		a.json(t)["truncated"] != true || a.json(t)["period_seconds"] != 86400.0 {
		t.Errorf("unsaved = %d %s", a.status, a.body)
	}
	req := fr.previews[0]
	if req.RouteID != "" || len(req.Matchers) != 1 || req.Matchers[0] != (routing.Matcher{Label: "alertname", Op: "=",
		Value: "Disk"}) || !slices.Equal(req.ProposedGroupKey, []string{"alertname", "cluster"}) || req.PeriodSeconds != nil {
		t.Errorf("request %+v", req)
	}
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"`+routeID+`","proposed_group_key":[],"period_seconds":3600,"matchers":[]}`)
	var p gen.GroupKeyPreview
	decodeInto(t, a, &p)
	if a.status != http.StatusOK || p.Current == nil || p.Current.AlertGroupCount != 1 || p.Proposed.AlertGroupCount != 2 {
		t.Errorf("saved = %d %s", a.status, a.body)
	}
	req = fr.previews[1]
	if req.RouteID != routeID || req.Matchers == nil || len(req.Matchers) != 0 || *req.PeriodSeconds != 3600 {
		t.Errorf("request %+v", req)
	}
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":null,"proposed_group_key":["alertname"]}`)
	errs, _ := a.json(t)["errors"].([]any)
	first, _ := errs[0].(map[string]any)
	if a.status != http.StatusUnprocessableEntity || first["code"] != "one_of_required" || fr.previews[2].RouteID != "" {
		t.Errorf("neither = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"RTZZZZZZZZZZZZ","proposed_group_key":[]}`); a.status != http.StatusNotFound {
		t.Errorf("unknown route = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"`+routeID+`","proposed_group_key":[],"period_seconds":0}`); a.status != http.StatusBadRequest {
		t.Errorf("a period below the minimum = %d", a.status)
	}
	if a = x.as(t, routesReader, http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"`+routeID+`","proposed_group_key":[]}`); a.status != http.StatusForbidden {
		t.Errorf("preview by a viewer = %d", a.status)
	}
}

// TestRouteSuggestionsAPI is C-08.FR-11, C-08.AC-8 and C-08.AC-11 at the API: the list for routes:read with the
// calling User's dismissals, acceptance for routes:write with the created Route, its ETag and Location, 409
// suggestion_obsolete, and dismissal for routes:read, refused to a Service account.
func TestRouteSuggestionsAPI(t *testing.T) {
	x, fr := newRoutesAPI(t)
	fr.suggestionOK = true
	a := x.as(t, routesReader, http.MethodGet, "/api/v1/route-suggestions", "")
	var list gen.RouteSuggestionList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 1 || list.Items[0].Id != gen.RouteSuggestionIdHeartbeatLost ||
		list.Items[0].Route.Name != "Muster: Heartbeat lost" || !list.Items[0].Route.Urgent ||
		list.Items[0].Route.DestinationIds == nil || *list.Items[0].Route.Description != "" ||
		list.Items[0].Route.Matchers[0].Value != "MusterHeartbeatLost" || fr.users[0] == nil {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesService, http.MethodGet, "/api/v1/route-suggestions", ""); a.status != http.StatusOK ||
		fr.users[1] != nil {
		t.Errorf("list by a service account = %d %s", a.status, a.body)
	}

	path := "/api/v1/route-suggestions/heartbeat_lost/"
	a = x.as(t, routesWriter, http.MethodPost, path+"accept", "")
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/routes/"+routeID || a.json(t)["name"] != "Muster: Heartbeat lost" ||
		!slices.Equal(fr.accepted[0], []string{"heartbeat_lost"}) || fr.by[0].Actor.TokenName != "routes" {
		t.Errorf("accept = %d %v %s", a.status, a.header, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPost, path+"accept", `{"destination_ids":["DSAAAAAAAAAAAA"]}`); a.status !=
		http.StatusCreated || !slices.Equal(fr.accepted[1], []string{"heartbeat_lost", "DSAAAAAAAAAAAA"}) {
		t.Errorf("accept with destinations = %d %v", a.status, fr.accepted)
	}
	a = x.as(t, routesWriter, http.MethodPost, "/api/v1/route-suggestions/internal_alerts/accept",
		`{"destination_ids":[]}`)
	if a.status != http.StatusConflict || a.code(t) != "suggestion_obsolete" {
		t.Errorf("obsolete = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesReader, http.MethodPost, path+"accept", ""); a.status != http.StatusForbidden {
		t.Errorf("accept by a viewer = %d", a.status)
	}
	if a = x.as(t, routesWriter, http.MethodPost, "/api/v1/route-suggestions/other/accept", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("accept an id outside the enum = %d", a.status)
	}

	if a = x.as(t, routesReader, http.MethodPost, path+"dismiss", ""); a.status != http.StatusNoContent ||
		len(fr.dismissed) != 1 {
		t.Errorf("dismiss = %d %s", a.status, a.body)
	}
	a = x.as(t, routesService, http.MethodPost, path+"dismiss", "")
	if a.status != http.StatusForbidden || a.code(t) != "service_account_not_allowed" || len(fr.dismissed) != 1 {
		t.Errorf("dismiss by a service account = %d %s", a.status, a.body)
	}
	fr.suggestionOK = false
	if a = x.as(t, routesReader, http.MethodPost, path+"dismiss", ""); a.status != http.StatusConflict ||
		a.code(t) != "suggestion_obsolete" {
		t.Errorf("dismiss obsolete = %d %s", a.status, a.body)
	}
	if a = x.as(t, routesWriter, http.MethodPost, path+"accept", ""); a.status != http.StatusConflict {
		t.Errorf("accept obsolete = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/route-suggestions", "", bearer(readOnlyToken)...); a.status !=
		http.StatusForbidden {
		t.Errorf("list without routes:read = %d", a.status)
	}
	fr.err = errors.New("boom")
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/route-suggestions"},
		{http.MethodPost, "/api/v1/group-key-previews"},
	} {
		if a = x.as(t, routesWriter, tt.method, tt.path, `{"matchers":[],"proposed_group_key":[]}`); a.status !=
			http.StatusInternalServerError {
			t.Errorf("%s %s with a failure = %d", tt.method, tt.path, a.status)
		}
	}
}

// TestSuggestionProblems maps the errors of the suggestions to their problems.
func TestSuggestionProblems(t *testing.T) {
	x, _ := newRoutesAPI(t)
	if p := x.srv.problemFor(t.Context(), "", routing.ErrSuggestionNotFound); p.Status != http.StatusNotFound {
		t.Errorf("not found = %+v", p)
	}
}

// TestRouteStormAPI is C-11.FR-6 on the Route: storm_active and storm, since when and how many new Alert Groups its
// Storm summary counts, while a Storm is active; storm_active false and no storm otherwise.
func TestRouteStormAPI(t *testing.T) {
	x, fr := newRoutesAPI(t)
	fr.list.Routes[0].Storm = &routing.Storm{Since: t0, AlertGroupCount: 21}
	a := x.as(t, routesReader, http.MethodGet, "/api/v1/routes", "")
	var list gen.RouteList
	decodeInto(t, a, &list)
	if p, d := list.Items[0], list.Items[1]; a.status != http.StatusOK || !p.StormActive || p.Storm == nil ||
		!p.Storm.Since.Equal(t0) || p.Storm.AlertGroupCount != 21 || d.StormActive || d.Storm != nil {
		t.Errorf("list = %d %s", a.status, a.body)
	}
}
