// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

const (
	scheduleID         = "SC0000000000MA"
	overrideID         = "VR0000000000AA"
	schedulesAdmin     = "mstr_pat_schedules_admin"
	schedulesResponder = "mstr_pat_schedules_responder"
	schedulesViewer    = "mstr_pat_schedules_viewer"
	schedulesSA        = "mstr_sat_schedules"
)

var (
	apiPetr = schedules.Person{ID: 2, PublicID: bobPublicID, Name: "Petr", Login: "petr", Role: "responder",
		Status: "active"}
	apiOlga = schedules.Person{ID: 5, PublicID: "SRCCCCCCCCCCCC", Name: "Olga", Login: "olga", Role: "responder",
		Status: "deleted"}
)

// fakeSchedules stands for internal/schedules: one Schedule with two Rotation layers, its On-call and an Override, and
// what the API asked.
type fakeSchedules struct {
	schedule  schedules.Schedule
	override  schedules.Override
	result    schedules.Result
	filters   []schedules.ListFilter
	inputs    []schedules.Input
	overrides []schedules.OverrideInput
	periods   [][2]*time.Time
	listed    []schedules.OverrideFilter
	versions  []*int64
	by        []schedules.Requester
	deleted   []string
	err       error
}

func newFakeSchedules() *fakeSchedules {
	low := schedules.Layer{PublicID: "RY0000000000L1", Number: 1, Name: "Around the clock",
		Users: []schedules.Person{apiPetr, apiOlga}, ShiftLength: schedules.ShiftWeek, HandoverTime: "10:00",
		HandoverDay: "monday", StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	high := schedules.Layer{PublicID: "RY0000000000H2", Number: 2, Name: "Nights", Users: []schedules.Person{apiOlga},
		ShiftLength: schedules.ShiftHours, ShiftSeconds: 43200, HandoverTime: "19:00",
		StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		Limits:    &schedules.Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}}
	sc := schedules.Schedule{ID: 1, PublicID: scheduleID, Name: "Main", TimeZone: "Europe/Berlin", Main: true,
		Team:   schedules.TeamRef{ID: 1, PublicID: teamID, Name: "Payments", Slug: "payments"},
		Layers: []schedules.Layer{low, high}, CreatedAt: t0, Version: 3}
	o := schedules.Override{ID: 1, PublicID: overrideID, User: apiPetr, Replaces: &apiOlga, Start: t0,
		End: t0.Add(time.Hour), CreatedBy: schedules.Actor{Kind: "user", PublicID: bobPublicID, Name: "Petr",
			Login: "petr"}, CreatedAt: t0, Version: 2}
	ref := schedules.LayerRef{PublicID: low.PublicID, Name: low.Name}
	res := schedules.Result{From: t0, To: t0.Add(3 * time.Hour),
		Segments: []schedules.Segment{
			{Start: t0, End: t0.Add(time.Hour), User: &apiPetr,
				Source: &schedules.Source{Kind: schedules.SourceOverride, Override: overrideID}},
			{Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour), User: &apiPetr,
				Source: &schedules.Source{Kind: schedules.SourceRotationLayer, Layer: ref}},
			{Start: t0.Add(2 * time.Hour), End: t0.Add(3 * time.Hour)}},
		Layers: []schedules.LayerShifts{{Layer: ref, Shifts: []schedules.LayerShift{
			{Shift: schedules.Shift{Start: t0, End: t0.Add(2 * time.Hour), User: apiPetr}},
			{Shift: schedules.Shift{Start: t0.Add(2 * time.Hour), End: t0.Add(3 * time.Hour), User: apiOlga},
				Skipped: schedules.ReasonNotInTeam}}}},
		Overrides: []schedules.Override{o},
		Warnings: []schedules.Warning{{Kind: schedules.ReasonNotInTeam, User: apiOlga, Layer: &ref},
			{Kind: schedules.ReasonDisabled, User: apiOlga, Override: overrideID}}}
	return &fakeSchedules{schedule: sc, override: o, result: res}
}

func (f *fakeSchedules) List(_ context.Context, lf schedules.ListFilter) (schedules.Page, error) {
	f.filters = append(f.filters, lf)
	return schedules.Page{Schedules: []schedules.Schedule{f.schedule},
		Next: &schedules.Position{Name: "main", ID: 1}}, f.err
}

func (f *fakeSchedules) Get(_ context.Context, id string) (schedules.Schedule, error) {
	if id != scheduleID {
		return schedules.Schedule{}, schedules.ErrNotFound
	}
	return f.schedule, f.err
}

func (f *fakeSchedules) Create(_ context.Context, r schedules.Requester, in schedules.Input) (schedules.Schedule,
	error) {
	f.inputs, f.by = append(f.inputs, in), append(f.by, r)
	return f.schedule, f.err
}

func (f *fakeSchedules) Update(_ context.Context, r schedules.Requester, _ string, version *int64,
	in schedules.Input) (schedules.Schedule, error) {
	f.inputs, f.versions, f.by = append(f.inputs, in), append(f.versions, version), append(f.by, r)
	return f.schedule, f.err
}

func (f *fakeSchedules) Delete(_ context.Context, r schedules.Requester, id string, version *int64) error {
	f.deleted, f.versions, f.by = append(f.deleted, id), append(f.versions, version), append(f.by, r)
	return f.err
}

func (f *fakeSchedules) OnCall(_ context.Context, _ string, from, to *time.Time) (schedules.Result, error) {
	f.periods = append(f.periods, [2]*time.Time{from, to})
	return f.result, f.err
}

func (f *fakeSchedules) Overrides(_ context.Context, _ string, of schedules.OverrideFilter) (schedules.OverridePage,
	error) {
	f.listed = append(f.listed, of)
	return schedules.OverridePage{Overrides: []schedules.Override{f.override},
		Next: &schedules.OverridePosition{Start: t0, ID: 1}}, f.err
}

func (f *fakeSchedules) CreateOverride(_ context.Context, r schedules.Requester, _ string,
	in schedules.OverrideInput) (schedules.Override, error) {
	f.overrides, f.by = append(f.overrides, in), append(f.by, r)
	return f.override, f.err
}

func (f *fakeSchedules) UpdateOverride(_ context.Context, r schedules.Requester, _, _ string, version *int64,
	in schedules.OverrideInput) (schedules.Override, error) {
	f.overrides, f.versions, f.by = append(f.overrides, in), append(f.versions, version), append(f.by, r)
	return f.override, f.err
}

func (f *fakeSchedules) DeleteOverride(_ context.Context, r schedules.Requester, _, id string, version *int64) error {
	f.deleted, f.versions, f.by = append(f.deleted, id), append(f.versions, version), append(f.by, r)
	return f.err
}

// newSchedulesAPI adds the fake Schedules and four tokens: an Admin's, a Responder's, a Viewer's and a Service
// account's that is not an Admin.
func newSchedulesAPI(t *testing.T) (*testAPI, *fakeSchedules) {
	t.Helper()
	x, _, _ := newTeamsAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	owner := ft.idents[fullToken].Session
	responder := auth.Session{User: auth.Principal{ID: 2, PublicID: bobPublicID, Name: "Petr",
		Role: auth.RoleResponder}}
	viewer := auth.Session{User: auth.Principal{ID: 4, PublicID: "SRDDDDDDDDDDDD", Name: "Vic",
		Role: auth.RoleViewer}}
	ft.idents[schedulesAdmin] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"schedules:read",
		"schedules:write", "schedule-overrides:write"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 71, Name: "schedules"}}
	ft.idents[schedulesResponder] = &auth.Identity{Session: responder, Permissions: []auth.Permission{
		"schedules:read", "schedule-overrides:write"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 72, Name: "on-call"}}
	ft.idents[schedulesViewer] = &auth.Identity{Session: viewer, Permissions: []auth.Permission{"schedules:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 73, Name: "read"}}
	ft.idents[schedulesSA] = &auth.Identity{Permissions: []auth.Permission{"schedules:read",
		"schedule-overrides:write"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 74, Name: "ci",
		ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "responder"}}}
	fs := newFakeSchedules()
	x.srv.schedules = fs
	return x, fs
}

const scheduleBody = `{"team_id":"` + teamID + `","name":"Main","time_zone":"Europe/Berlin","main":true,
	"rotation_layers":[{"id":"RY0000000000L1","name":"Around the clock","user_ids":["` + bobPublicID + `"],
	"shift_length":"week","handover_time":"10:00","handover_day":"monday","start_date":"2026-11-02","limits":null},
	{"name":"Nights","user_ids":["SRCCCCCCCCCCCC"],"shift_length":"hours","shift_seconds":43200,
	"handover_time":"19:00","start_date":"2026-11-02","limits":{"days":["friday"],"start":"19:00","end":"09:00"}}]}`

const overrideBody = `{"user_id":"` + bobPublicID + `","replaces_user_id":"SRCCCCCCCCCCCC",
	"start":"2026-11-18T10:00:00Z","end":"2026-11-18T20:00:00Z"}`

// testSchedulesAPI is C-27.FR-1, FR-2 and FR-13 at the API: the list with its cursor, team and q, a Schedule with its
// Rotation layers, ETag and Location on creation, If-Match on update (428 without), the deletion, and the refusals of
// the domain as problems.
func testSchedulesAPI(t *testing.T, x *testAPI, fs *fakeSchedules) {
	a := x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules?team="+teamID+"&q=ma&limit=1", "")
	var list struct {
		Items []struct {
			ID             string `json:"id"`
			Main           bool   `json:"main"`
			Team           map[string]any
			RotationLayers []struct {
				Number       int               `json:"number"`
				Users        []json.RawMessage `json:"users"`
				ShiftSeconds *int              `json:"shift_seconds"`
				HandoverDay  *string           `json:"handover_day"`
				Limits       *struct {
					Days       []string `json:"days"`
					Start, End string
				} `json:"limits"`
			} `json:"rotation_layers"`
			Etag string `json:"etag"`
		} `json:"items"`
		Next string `json:"next_cursor"`
	}
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 1 || fs.filters[0].Team != teamID ||
		fs.filters[0].Query != "ma" || fs.filters[0].Limit != 1 || list.Items[0].Etag != `"3"` ||
		len(list.Items[0].RotationLayers) != 2 || list.Items[0].RotationLayers[0].Limits != nil ||
		*list.Items[0].RotationLayers[0].HandoverDay != "monday" || *list.Items[0].RotationLayers[1].ShiftSeconds != 43200 ||
		list.Items[0].RotationLayers[1].Limits.End != "09:00" || list.Items[0].Team["slug"] != "payments" {
		t.Fatalf("list = %d %s, %+v", a.status, a.body, fs.filters)
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules?cursor="+list.Next, ""); a.status !=
		http.StatusOK || *fs.filters[1].After != (schedules.Position{Name: "main", ID: 1}) {
		t.Errorf("second page = %d, %+v", a.status, fs.filters[1])
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules?team=mine", ""); a.status != http.StatusOK ||
		fs.filters[2].MemberID != 4 {
		t.Errorf("mine = %d, %+v", a.status, fs.filters[2])
	}
	if status, _, ptr, code := firstProblemError(t, x.as(t, schedulesSA, http.MethodGet, "/api/v1/schedules?team=mine",
		"")); status != http.StatusUnprocessableEntity || ptr != "/query/team" || code != fieldUnsupported {
		t.Errorf("mine of a service account = %d %s %s", status, ptr, code)
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules?cursor=x", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}

	a = x.as(t, schedulesAdmin, http.MethodPost, "/api/v1/schedules", scheduleBody)
	if a.status != http.StatusCreated || a.header.Get("Location") != "/api/v1/schedules/"+scheduleID ||
		a.header.Get("ETag") != `"3"` {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := fs.inputs[0]
	if in.TeamID != teamID || *in.TimeZone != "Europe/Berlin" || !*in.Main || len(in.Layers) != 2 ||
		in.Layers[0].ID != "RY0000000000L1" || in.Layers[0].HandoverDay != "monday" || in.Layers[0].Limits != nil ||
		in.Layers[1].ShiftLength != schedules.ShiftHours || *in.Layers[1].ShiftSeconds != 43200 ||
		in.Layers[1].Limits.Start != "19:00" || in.Layers[1].Limits.Days[0] != "friday" ||
		in.Layers[1].StartDate.Format(time.DateOnly) != "2026-11-02" || !fs.by[0].Admin {
		t.Errorf("input %+v by %+v", in, fs.by[0])
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules/"+scheduleID, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"3"` {
		t.Errorf("get = %d %v", a.status, a.header)
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules/SC0000000000ZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
	if a = x.as(t, schedulesAdmin, http.MethodPut, "/api/v1/schedules/"+scheduleID, scheduleBody); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, schedulesAdmin, http.MethodPut, "/api/v1/schedules/"+scheduleID, `{"team_id":"`+teamID+
		`","name":"Main","rotation_layers":[]}`, "If-Match", `"3"`); a.status != http.StatusOK ||
		*fs.versions[0] != 3 || fs.inputs[1].TimeZone != nil || fs.inputs[1].Main != nil {
		t.Errorf("update = %d %s, %+v", a.status, a.body, fs.inputs[1])
	}
	if a = x.as(t, schedulesAdmin, http.MethodDelete, "/api/v1/schedules/"+scheduleID, "", "If-Match", `"3"`); a.status !=
		http.StatusNoContent || fs.deleted[0] != scheduleID {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	for _, c := range []struct {
		err          error
		status       int
		code, ptr, f string
	}{
		{schedules.ErrNameTaken, http.StatusConflict, codeNameTaken, "", ""},
		{&schedules.FieldError{Pointer: "/rotation_layers/1/limits/end", Code: schedules.CodeOutOfRange},
			http.StatusUnprocessableEntity, "", "/rotation_layers/1/limits/end", "out_of_range"},
		{&schedules.FieldError{Pointer: "/team_id", Code: schedules.CodeUnsupported},
			http.StatusUnprocessableEntity, "", "/team_id", "unsupported"},
	} {
		fs.err = c.err
		status, code, ptr, f := firstProblemError(t, x.as(t, schedulesAdmin, http.MethodPost, "/api/v1/schedules",
			scheduleBody))
		if status != c.status || code != c.code || ptr != c.ptr || f != c.f {
			t.Errorf("%v = %d %s %s %s", c.err, status, code, ptr, f)
		}
	}
	fs.err = &schedules.FieldError{Pointer: "/rotation_layers/0/user_ids/0", Code: schedules.CodeNotPageable,
		Reason: schedules.ReasonViewer}
	a = x.as(t, schedulesAdmin, http.MethodPost, "/api/v1/schedules", scheduleBody)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body),
		`"code":"not_pageable"`) || !strings.Contains(string(a.body), `"reason":"viewer"`) {
		t.Errorf("not pageable = %d %s", a.status, a.body)
	}
	fs.err = schedules.ErrVersionMismatch
	if a = x.as(t, schedulesAdmin, http.MethodPut, "/api/v1/schedules/"+scheduleID, scheduleBody, "If-Match",
		`"1"`); a.status != http.StatusPreconditionFailed {
		t.Errorf("stale = %d %s", a.status, a.body)
	}
	fs.err = &schedules.InUseError{Referrers: []schedules.Referrer{{Type: "escalation_chain",
		PublicID: "ECAAAAAAAAAAAA", Name: "Payments"}}}
	a = x.as(t, schedulesAdmin, http.MethodDelete, "/api/v1/schedules/"+scheduleID, "")
	var held struct {
		Code    string `json:"code"`
		Detail  string `json:"detail"`
		InUseBy []struct {
			Type, ID, Name string
		} `json:"in_use_by"`
	}
	decodeInto(t, a, &held)
	if a.status != http.StatusConflict || held.Code != codeScheduleInUse || len(held.InUseBy) != 1 ||
		held.InUseBy[0].Type != "escalation_chain" || !strings.Contains(held.Detail, `"Payments"`) {
		t.Errorf("schedule in use = %d %s", a.status, a.body)
	}
	fs.err = schedules.ErrInUse
	if status, code, _, _ := firstProblemError(t, x.as(t, schedulesAdmin, http.MethodDelete,
		"/api/v1/schedules/"+scheduleID, "")); status != http.StatusConflict || code != codeScheduleInUse {
		t.Errorf("schedule in use without referrers = %d %s", status, code)
	}
	fs.err = errors.New("boom")
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/schedules", ""},
		{http.MethodGet, "/api/v1/schedules/" + scheduleID, ""},
		{http.MethodPut, "/api/v1/schedules/" + scheduleID, scheduleBody},
		{http.MethodDelete, "/api/v1/schedules/" + scheduleID, ""},
		{http.MethodGet, "/api/v1/schedules/" + scheduleID + "/on-call", ""},
		{http.MethodGet, "/api/v1/schedules/" + scheduleID + "/overrides", ""},
		{http.MethodPost, "/api/v1/schedules/" + scheduleID + "/overrides", overrideBody},
		{http.MethodPut, "/api/v1/schedules/" + scheduleID + "/overrides/" + overrideID, overrideBody},
		{http.MethodDelete, "/api/v1/schedules/" + scheduleID + "/overrides/" + overrideID, ""},
	} {
		if a = x.as(t, schedulesAdmin, r.method, r.path, r.body, "If-Match", `"2"`); a.status !=
			http.StatusInternalServerError {
			t.Errorf("%s %s failed = %d", r.method, r.path, a.status)
		}
	}
}

// testScheduleOnCallAPI is C-27.FR-4 and FR-5 at the API: the segments with their person and source, or a gap, the
// Shifts of each Rotation layer with the skipped ones, the Overrides and the warnings; from and to are passed on, and a
// period the domain refuses is 422 out_of_range at /query/to.
func testScheduleOnCallAPI(t *testing.T, x *testAPI, fs *fakeSchedules) {
	a := x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules/"+scheduleID+
		"/on-call?from=2026-11-02T00:00:00Z&to=2026-11-09T00:00:00Z", "")
	var res struct {
		Segments []struct {
			User   *struct{ Login string } `json:"user"`
			Source *struct {
				Kind          string                 `json:"kind"`
				OverrideID    string                 `json:"override_id"`
				RotationLayer *struct{ Name string } `json:"rotation_layer"`
			} `json:"source"`
		} `json:"segments"`
		RotationLayers []struct {
			Shifts []struct {
				Skipped *string `json:"skipped"`
			} `json:"shifts"`
		} `json:"rotation_layers"`
		Overrides []struct {
			ReplacesUser *struct{ Deactivated bool } `json:"replaces_user"`
		} `json:"overrides"`
		Warnings []struct {
			Kind          string  `json:"kind"`
			RotationLayer *any    `json:"rotation_layer"`
			OverrideID    *string `json:"override_id"`
		} `json:"warnings"`
	}
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || len(res.Segments) != 3 || res.Segments[0].Source.Kind != "override" ||
		res.Segments[0].Source.OverrideID != overrideID || res.Segments[1].Source.RotationLayer.Name !=
		"Around the clock" || res.Segments[2].User != nil || res.Segments[2].Source != nil ||
		res.RotationLayers[0].Shifts[0].Skipped != nil || *res.RotationLayers[0].Shifts[1].Skipped != "not_in_team" ||
		!res.Overrides[0].ReplacesUser.Deactivated || res.Warnings[0].OverrideID != nil ||
		res.Warnings[1].RotationLayer != nil || *res.Warnings[1].OverrideID != overrideID {
		t.Fatalf("on-call = %d %s", a.status, a.body)
	}
	if p := fs.periods[0]; p[0] == nil || p[1] == nil || p[0].Format(time.RFC3339) != "2026-11-02T00:00:00Z" {
		t.Errorf("period %+v", p)
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules/"+scheduleID+"/on-call", ""); a.status !=
		http.StatusOK || fs.periods[1][0] != nil || fs.periods[1][1] != nil {
		t.Errorf("default period = %d, %+v", a.status, fs.periods[1])
	}
	fs.err = &schedules.FieldError{Pointer: "/query/to", Code: schedules.CodeOutOfRange}
	if status, _, ptr, code := firstProblemError(t, x.as(t, schedulesViewer, http.MethodGet,
		"/api/v1/schedules/"+scheduleID+"/on-call", "")); status != http.StatusUnprocessableEntity ||
		ptr != "/query/to" || code != "out_of_range" {
		t.Errorf("a period too long = %d %s %s", status, ptr, code)
	}
	fs.err = schedules.ErrNotFound
	if a = x.as(t, schedulesViewer, http.MethodGet, "/api/v1/schedules/"+scheduleID+"/on-call", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// testScheduleOverridesAPI is C-27.FR-3, FR-6 and C-27.AC-3 at the API: the Overrides listed with their cursor, an
// Override created by a Responder with its people, changed with If-Match and deleted, and the refusals not_in_team,
// override_ended, a stale If-Match and not_pageable at /user_id.
func testScheduleOverridesAPI(t *testing.T, x *testAPI, fs *fakeSchedules) {
	base := "/api/v1/schedules/" + scheduleID + "/overrides"
	a := x.as(t, schedulesViewer, http.MethodGet, base+"?from=2026-11-01T00:00:00Z&to=2026-12-01T00:00:00Z&limit=1",
		"")
	m := a.json(t)
	items, _ := m["items"].([]any)
	if a.status != http.StatusOK || len(items) != 1 || fs.listed[0].From == nil || fs.listed[0].To == nil ||
		fs.listed[0].Limit != 1 {
		t.Fatalf("list = %d %s, %+v", a.status, a.body, fs.listed)
	}
	o := items[0].(map[string]any)
	by, _ := o["created_by"].(map[string]any)
	if o["id"] != overrideID || o["replaces_user_id"] != "SRCCCCCCCCCCCC" || by["login"] != "petr" ||
		o["etag"] != `"2"` {
		t.Errorf("override %v", o)
	}
	cursor, _ := m["next_cursor"].(string)
	if a = x.as(t, schedulesViewer, http.MethodGet, base+"?cursor="+cursor, ""); a.status != http.StatusOK ||
		fs.listed[1].After == nil || fs.listed[1].After.ID != 1 {
		t.Errorf("second page = %d, %+v", a.status, fs.listed[1])
	}
	if a = x.as(t, schedulesViewer, http.MethodGet, base+"?cursor=x", ""); a.status != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}
	a = x.as(t, schedulesResponder, http.MethodPost, base, overrideBody)
	if a.status != http.StatusCreated || a.header.Get("Location") != base+"/"+overrideID ||
		a.header.Get("ETag") != `"2"` {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	if in := fs.overrides[0]; in.UserID != bobPublicID || in.ReplacesUserID != "SRCCCCCCCCCCCC" ||
		in.End.Format(time.RFC3339) != "2026-11-18T20:00:00Z" || fs.by[0].Admin ||
		fs.by[0].Actor.Kind != audit.ActorUser {
		t.Errorf("input %+v by %+v", in, fs.by[0])
	}
	if a = x.as(t, schedulesSA, http.MethodPost, base, `{"user_id":"`+bobPublicID+`","replaces_user_id":null,`+
		`"start":"2026-11-18T10:00:00Z","end":"2026-11-18T20:00:00Z"}`); a.status != http.StatusCreated ||
		fs.overrides[1].ReplacesUserID != "" || fs.by[1].Admin || fs.by[1].Actor.Kind != audit.ActorServiceAccount {
		t.Errorf("by a service account = %d %s, %+v", a.status, a.body, fs.by[1])
	}
	if a = x.as(t, schedulesResponder, http.MethodPut, base+"/"+overrideID, overrideBody); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, schedulesResponder, http.MethodPut, base+"/"+overrideID, overrideBody, "If-Match", `"2"`); a.status !=
		http.StatusOK || *fs.versions[0] != 2 || a.header.Get("ETag") != `"2"` {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = x.as(t, schedulesResponder, http.MethodDelete, base+"/"+overrideID, ""); a.status !=
		http.StatusNoContent || fs.deleted[0] != overrideID || fs.versions[1] != nil {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a = x.as(t, schedulesViewer, http.MethodPost, base, overrideBody); a.status != http.StatusForbidden ||
		!strings.Contains(string(a.body), "schedule-overrides:write") {
		t.Errorf("a viewer = %d %s", a.status, a.body)
	}
	for _, c := range []struct {
		err          error
		status       int
		code, ptr, f string
	}{
		{schedules.ErrNotInTeam, http.StatusConflict, codeNotInTeam, "", ""},
		{schedules.ErrOverrideEnded, http.StatusConflict, codeOverrideEnded, "", ""},
		{schedules.ErrOverrideNotFound, http.StatusNotFound, "", "", ""},
		{schedules.ErrVersionMismatch, http.StatusPreconditionFailed, "", "", ""},
		{&schedules.FieldError{Pointer: "/user_id", Code: schedules.CodeNotPageable,
			Reason: schedules.ReasonDisabled}, http.StatusUnprocessableEntity, "", "/user_id", "not_pageable"},
		{&schedules.FieldError{Pointer: "/end", Code: schedules.CodeOutOfRange}, http.StatusUnprocessableEntity, "",
			"/end", "out_of_range"},
	} {
		fs.err = c.err
		status, code, ptr, f := firstProblemError(t, x.as(t, schedulesResponder, http.MethodPut, base+"/"+overrideID,
			overrideBody, "If-Match", `"2"`))
		if status != c.status || code != c.code || ptr != c.ptr || f != c.f {
			t.Errorf("%v = %d %s %s %s", c.err, status, code, ptr, f)
		}
	}
}

// testSchedulesPermissions is C-27.FR-13 and C-27.AC-10 at the API: a Responder reads Schedules and the On-call and
// makes Overrides, and gets 403 on createSchedule, updateSchedule and deleteSchedule; a Viewer only reads.
func testSchedulesPermissions(t *testing.T, x *testAPI, fs *fakeSchedules) {
	for _, token := range []string{schedulesResponder, schedulesViewer} {
		for _, path := range []string{"/api/v1/schedules", "/api/v1/schedules/" + scheduleID,
			"/api/v1/schedules/" + scheduleID + "/on-call", "/api/v1/schedules/" + scheduleID + "/overrides"} {
			if a := x.as(t, token, http.MethodGet, path, ""); a.status != http.StatusOK {
				t.Errorf("%s reads %s = %d", token, path, a.status)
			}
		}
		for _, r := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/schedules", scheduleBody},
			{http.MethodPut, "/api/v1/schedules/" + scheduleID, scheduleBody},
			{http.MethodDelete, "/api/v1/schedules/" + scheduleID, ""},
		} {
			a := x.as(t, token, r.method, r.path, r.body, "If-Match", `"3"`)
			if a.status != http.StatusForbidden || !strings.Contains(string(a.body), "schedules:write") {
				t.Errorf("%s %s %s = %d %s", token, r.method, r.path, a.status, a.body)
			}
		}
	}
	if len(fs.inputs)+len(fs.deleted) != 0 {
		t.Error("a refused change reached the Schedules")
	}
}

// testTeamInUseProblem is C-25.FR-10 and AC-9 at the API: deleteTeam of a Team with Schedules is 409 team_in_use
// naming them in in_use_by.
func testTeamInUseProblem(t *testing.T, x *testAPI, _ *fakeSchedules) {
	ft := x.srv.teams.(*fakeTeams)
	ft.err = &teams.InUseError{Holders: []teams.Holder{{Type: teams.HolderSchedule, PublicID: scheduleID,
		Name: "Main"}, {Type: teams.HolderSchedule, PublicID: "SC0000000000NN", Name: "Nights"}}}
	a := x.as(t, teamsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID, "")
	var p struct {
		Code    string `json:"code"`
		Detail  string `json:"detail"`
		InUseBy []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"in_use_by"`
	}
	decodeInto(t, a, &p)
	if a.status != http.StatusConflict || p.Code != codeTeamInUse || len(p.InUseBy) != 2 ||
		p.InUseBy[0].Type != "schedule" || p.InUseBy[0].ID != scheduleID || p.InUseBy[1].Name != "Nights" ||
		!strings.Contains(p.Detail, `"Main", "Nights"`) {
		t.Errorf("team in use = %d %s", a.status, a.body)
	}
}

// TestSchedules runs the tests of the Schedules API on one server, which is slow to build under the race detector;
// each starts from a fresh fake.
func TestSchedules(t *testing.T) {
	x, fs := newSchedulesAPI(t)
	for _, c := range []struct {
		name string
		run  func(*testing.T, *testAPI, *fakeSchedules)
	}{
		{"schedules", testSchedulesAPI}, {"on-call", testScheduleOnCallAPI}, {"overrides", testScheduleOverridesAPI},
		{"permissions", testSchedulesPermissions}, {"team in use", testTeamInUseProblem},
	} {
		t.Run(c.name, func(t *testing.T) {
			*fs = *newFakeSchedules()
			x.srv.teams.(*fakeTeams).err = nil
			c.run(t, x, fs)
		})
	}
}
