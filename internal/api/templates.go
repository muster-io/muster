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
	if b.Kind == gen.TemplateKindLinkRule || b.Kind == gen.TemplateKindWebhookRequest {
		// A Link rule and an outgoing webhook request have no built-in template to start from.
		out.Source = nullable.NewNullNullable[string]()
	}
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
	if b.Kind == gen.TemplateKindRootMessage {
		out.Layout = nullable.NewNullNullable[gen.MessageLayout]()
		if res.Layout != nil {
			out.Layout = nullable.NewNullableWithValue(messageLayoutOf(*res.Layout))
			length := res.Length
			out.Length = &length
		}
		out.MessengerLimit = nullable.NewNullNullable[int]()
		if res.MessengerLimit > 0 {
			out.MessengerLimit = nullable.NewNullableWithValue(res.MessengerLimit)
		}
	}
	if res.Sample != "" {
		out.Sample = nullable.NewNullableWithValue(gen.TemplatePreviewResultSample(res.Sample))
	} else {
		out.Sample = nullable.NewNullNullable[gen.TemplatePreviewResultSample]()
	}
	return gen.PreviewTemplate200JSONResponse(out), nil
}

// messageLayoutOf is the MessageLayout of a previewed Root message (C-29.FR-1): the Assignees and the Owner come with
// the Escalation chains, and until then the footer, among the notices, names the Owner.
func messageLayoutOf(l messages.MessageLayout) gen.MessageLayout {
	out := gen.MessageLayout{Number: int(l.Number), Title: l.Title,
		Status:  gen.MessageLayoutStatus{Key: gen.MessageLayoutStatusKey(l.Status), Word: l.StatusWord},
		Summary: l.Summary, Description: l.Description, Fields: make([]gen.MessageLayoutField, 0, len(l.Fields)),
		Labels: make([]gen.MessageLayoutLabel, 0, len(l.Labels)), Notices: append([]string{}, l.Notices...),
		Assignees: []gen.MessageLayoutPerson{}, Links: make([]gen.MessageLayoutLink, 0, len(l.Links))}
	out.Owner = nullable.NewNullNullable[gen.MessageLayoutPerson]()
	out.Alerts = nullable.NewNullNullable[gen.MessageLayoutAlerts]()
	for _, f := range l.Fields {
		out.Fields = append(out.Fields, gen.MessageLayoutField{Name: f.Name, Value: f.Value})
	}
	for _, k := range l.Labels {
		out.Labels = append(out.Labels, gen.MessageLayoutLabel{Key: k.Name, Value: k.Value})
	}
	if a := l.Alerts; a != nil {
		values := make([]gen.MessageLayoutAlertValue, 0, len(a.Values))
		for _, v := range a.Values {
			values = append(values, gen.MessageLayoutAlertValue{Value: v.Value, Resolved: v.Resolved})
		}
		out.Alerts = nullable.NewNullableWithValue(gen.MessageLayoutAlerts{Label: a.Label, Values: values,
			Total: int(a.Total)})
	}
	for _, k := range l.Links {
		out.Links = append(out.Links, gen.MessageLayoutLink{Label: k.Text, Url: k.URL})
	}
	return out
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
