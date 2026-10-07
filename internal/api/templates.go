// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
)

// Templates previews templates (C-12.FR-5); *messages.Renderer implements it.
type Templates interface {
	Preview(ctx context.Context, req messages.PreviewRequest) (messages.PreviewResult, error)
}

// PreviewTemplate is previewTemplate: a template rendered in the sandbox against its sample. A template that fails
// answers 200 with valid false and its errors, never a Problem.
func (s *Server) PreviewTemplate(ctx context.Context, req gen.PreviewTemplateRequestObject) (
	gen.PreviewTemplateResponseObject, error) {
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	b := req.Body
	in := messages.PreviewRequest{Kind: string(b.Kind), Template: b.Template, Format: messages.Markup(value(b.Format)),
		RouteID: value(b.RouteId), SnapshotID: value(b.StoredSnapshotId), AlertGroupID: value(b.AlertGroupId),
		Language: string(value(b.Language)), LengthLimit: value(b.LengthLimit)}
	res, err := s.templates.Preview(ctx, in)
	switch {
	case errors.Is(err, messages.ErrUnsupportedKind):
		return nil, fieldProblem(http.StatusBadRequest, "/kind", fieldUnsupported,
			"Previews of this kind of template arrive with their capability.")
	case errors.Is(err, messages.ErrRouteNotFound):
		return nil, problem(http.StatusNotFound, typeNotFound, "", "No such Route.")
	case errors.Is(err, messages.ErrNotFound):
		return nil, problem(http.StatusNotFound, typeNotFound, "", "No such Alert Group.")
	case errors.Is(err, messages.ErrSnapshotNotFound):
		return nil, problem(http.StatusNotFound, typeNotFound, "", "No such Stored Snapshot, or it is past retention.")
	case err != nil:
		return nil, err
	}
	out := gen.TemplatePreviewResult{Valid: res.Valid, Errors: make([]gen.ProblemError, 0, len(res.Errors)),
		Source: nullable.NewNullableWithValue(res.Source),
		Format: nullable.NewNullNullable[gen.TemplatePreviewResultFormat]()}
	if res.Format != "" {
		out.Format = nullable.NewNullableWithValue(gen.TemplatePreviewResultFormat(res.Format))
	}
	for _, e := range res.Errors {
		out.Errors = append(out.Errors, problemErrorOf(e))
	}
	if res.Valid {
		out.Output = nullable.NewNullableWithValue(res.Output)
		truncated := res.Truncated
		out.Truncated = &truncated
	} else {
		out.Output = nullable.NewNullNullable[string]()
	}
	if res.Sample != "" {
		out.Sample = nullable.NewNullableWithValue(gen.TemplatePreviewResultSample(res.Sample))
	} else {
		out.Sample = nullable.NewNullNullable[gen.TemplatePreviewResultSample]()
	}
	return gen.PreviewTemplate200JSONResponse(out), nil
}

// problemErrorOf is a template error as the API shows it: its code, its position and what failed, at the template.
func problemErrorOf(e *templates.Error) gen.ProblemError {
	detail, pointer := e.Detail, "/template"
	if e.Code == messages.CodeInvalidSample {
		pointer = "/stored_snapshot_id"
	}
	return gen.ProblemError{Pointer: pointer, Code: e.Code, Detail: &detail, Line: positive(e.Line),
		Column: positive(e.Column)}
}

// value is the value of an optional field, its zero value when it is absent or null.
func value[T any](v nullable.Nullable[T]) T {
	if !v.IsSpecified() || v.IsNull() {
		var zero T
		return zero
	}
	return v.MustGet()
}
