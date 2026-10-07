// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// fakeTemplates stands for the renderer: it records the requests and answers by the template.
type fakeTemplates struct {
	reqs []messages.PreviewRequest
}

func (f *fakeTemplates) Preview(_ context.Context, req messages.PreviewRequest) (messages.PreviewResult, error) {
	f.reqs = append(f.reqs, req)
	switch req.Template {
	case `{{ env "HOME" }}`:
		return messages.PreviewResult{Format: messages.MarkupMarkdown, Source: req.Template,
			Errors: []*templates.Error{{Code: "unknown_function", Line: 1, Column: 4, Detail: `"env"`}}}, nil
	case "sample":
		return messages.PreviewResult{Errors: []*templates.Error{{Code: messages.CodeInvalidSample, Detail: "x"}}}, nil
	case "route":
		return messages.PreviewResult{}, messages.ErrRouteNotFound
	case "group":
		return messages.PreviewResult{}, messages.ErrNotFound
	case "snapshot":
		return messages.PreviewResult{}, messages.ErrSnapshotNotFound
	case "kind":
		return messages.PreviewResult{}, messages.ErrUnsupportedKind
	case "boom":
		return messages.PreviewResult{}, errors.New("boom")
	}
	return messages.PreviewResult{Valid: true, Output: "🔴 [#1 A](u)", Truncated: true, Format: req.Format,
		Source: "built-in", Sample: messages.SampleAlertGroup}, nil
}

// The token of the preview tests, with templates:preview.
const previewToken = "mstr_pat_templates_preview"

// TestPreviewTemplateAPI is previewTemplate (C-12.FR-5, C-12.AC-2): the request reaches the renderer with its
// fields; a template that fails answers 200 with valid false, its errors with line and column and no output; an
// unknown Route, Alert Group or Stored Snapshot is 404, a kind without previews 400; templates:preview is needed.
func TestPreviewTemplateAPI(t *testing.T) {
	x, ft, _, _ := newTokensAPI(t)
	ft.idents[previewToken] = &auth.Identity{Session: ft.idents[fullToken].Session,
		Permissions: []auth.Permission{"templates:preview"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 41, Name: "preview"}}
	fake := &fakeTemplates{}
	x.srv.templates = fake
	a := x.as(t, previewToken, http.MethodPost, "/api/v1/template-previews", `{"kind":"root_message",
		"template":"","format":"html","route_id":"RTAAAAAAAAAAAA","alert_group_id":"AGK7M3QX9P2RTA",
		"stored_snapshot_id":"SS0000000000AA","language":"ru","length_limit":4096}`)
	out := a.json(t)
	req := fake.reqs[0]
	if a.status != http.StatusOK || out["valid"] != true || out["truncated"] != true || out["output"] != "🔴 [#1 A](u)" ||
		out["format"] != "html" || out["source"] != "built-in" || out["sample"] != "alert_group" ||
		req != (messages.PreviewRequest{Kind: "root_message", Format: messages.MarkupHTML, RouteID: "RTAAAAAAAAAAAA",
			AlertGroupID: "AGK7M3QX9P2RTA", SnapshotID: "SS0000000000AA", Language: "ru", LengthLimit: 4096}) {
		t.Errorf("preview = %d %s, request %+v", a.status, a.body, req)
	}
	a = x.as(t, previewToken, http.MethodPost, "/api/v1/template-previews",
		`{"kind":"root_message","template":"{{ env \"HOME\" }}"}`)
	out = a.json(t)
	errs, _ := out["errors"].([]any)
	first, _ := errs[0].(map[string]any)
	if a.status != http.StatusOK || out["valid"] != false || out["output"] != nil || out["sample"] != nil ||
		first["code"] != "unknown_function" || first["line"] != float64(1) || first["column"] != float64(4) ||
		first["pointer"] != "/template" || fake.reqs[1].Format != "" {
		t.Errorf("invalid = %d %s", a.status, a.body)
	}
	a = x.as(t, previewToken, http.MethodPost, "/api/v1/template-previews", `{"kind":"line","template":"sample"}`)
	errs, _ = a.json(t)["errors"].([]any)
	if first, _ = errs[0].(map[string]any); first["pointer"] != "/stored_snapshot_id" {
		t.Errorf("sample = %s", a.body)
	}
	for template, status := range map[string]int{"route": http.StatusNotFound, "group": http.StatusNotFound,
		"snapshot": http.StatusNotFound, "kind": http.StatusBadRequest, "boom": http.StatusInternalServerError} {
		if a := x.as(t, previewToken, http.MethodPost, "/api/v1/template-previews",
			`{"kind":"line","template":"`+template+`"}`); a.status != status {
			t.Errorf("%s = %d %s", template, a.status, a.body)
		}
	}
	if a := x.as(t, readOnlyToken, http.MethodPost, "/api/v1/template-previews",
		`{"kind":"line","template":""}`); a.status != http.StatusForbidden {
		t.Errorf("without templates:preview = %d", a.status)
	}
	if a := x.as(t, previewToken, http.MethodPost, "/api/v1/template-previews", `{"kind":"nope","template":""}`); a.status !=
		http.StatusBadRequest {
		t.Errorf("an unknown kind = %d", a.status)
	}
}
