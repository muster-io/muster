// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/requestlog"
)

// RequestLog is what the API needs of the Request log reader in internal/requestlog.
type RequestLog interface {
	List(ctx context.Context, f requestlog.Filter) (requestlog.Page, error)
	Get(ctx context.Context, publicID string) (requestlog.Record, error)
}

// requestLogCursor names the cursors of listRequestLog.
const requestLogCursor = "request-log"

// requestLogKey is the sort key of a listRequestLog cursor.
type requestLogKey struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

var (
	errRequestLogOneOf = fieldProblem(http.StatusUnprocessableEntity, "/query/connection", codeOneOfRequired,
		"Give exactly one of connection and destination.")
	errRequestLogOwnerNotFound = problem(http.StatusNotFound, typeNotFound, "", "No such Connection or Destination.")
	errRequestRecordNotFound   = problem(http.StatusNotFound, typeNotFound, "", "No such request record.")
)

// codeOneOfRequired is the code of a request that gives neither or several of the fields of which it takes one.
const codeOneOfRequired = "one_of_required"

// ListRequestLog is listRequestLog (C-23.FR-5): the records of exactly one Connection or Destination, newest first, by
// cursor, filtered by direction, status class, operation and Alert Group. Naming the owner needs its Permission —
// connections:read for a Connection, destinations:read for a Destination — before its existence is told.
func (s *Server) ListRequestLog(ctx context.Context, req gen.ListRequestLogRequestObject) (
	gen.ListRequestLogResponseObject, error) {
	p := req.Params
	if (p.Connection == nil) == (p.Destination == nil) {
		return nil, errRequestLogOneOf
	}
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	need := gen.ConnectionsRead
	if p.Destination != nil {
		need = gen.DestinationsRead
	}
	if !id.Can(auth.Permission(need)) {
		return nil, forbidden(need)
	}
	f := requestlog.Filter{Limit: pageSize(p.Limit)}
	for _, v := range []struct {
		dst *string
		src *string
	}{{&f.Connection, p.Connection}, {&f.Destination, p.Destination}, {&f.Operation, p.Operation},
		{&f.AlertGroup, p.AlertGroup}} {
		if v.src != nil {
			*v.dst = *v.src
		}
	}
	if p.Direction != nil {
		f.Direction = string(*p.Direction)
	}
	if p.StatusClass != nil {
		f.StatusClass = string(*p.StatusClass)
	}
	var key requestLogKey
	if ok, err := decodeCursor(p.Cursor, requestLogCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &requestlog.Position{At: key.At, ID: key.ID}
	}
	page, err := s.requestLog.List(ctx, f)
	if errors.Is(err, requestlog.ErrNotFound) {
		return nil, errRequestLogOwnerNotFound
	}
	if errors.Is(err, requestlog.ErrOneOfRequired) {
		return nil, errRequestLogOneOf
	}
	if err != nil {
		return nil, err
	}
	out := gen.RequestLogList{Items: make([]gen.RequestRecordSummary, 0, len(page.Records))}
	for _, r := range page.Records {
		out.Items = append(out.Items, requestRecordSummaryOf(r))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(requestLogCursor, requestLogKey{At: page.Next.At, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListRequestLog200JSONResponse(out), nil
}

// GetRequestRecord is getRequestRecord (C-23.FR-5): one record with its headers and bodies. A record that is not
// listed — unknown, past either bound, or of a deleted owner — is 404; reading it needs the Permission of its owner:
// destinations:read for an outgoing webhook's, connections:read for a Connection's and for one without an owner.
func (s *Server) GetRequestRecord(ctx context.Context, req gen.GetRequestRecordRequestObject) (
	gen.GetRequestRecordResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := s.requestLog.Get(ctx, req.RequestRecordId)
	if errors.Is(err, requestlog.ErrNotFound) {
		return nil, errRequestRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	need := gen.ConnectionsRead
	if rec.Owner == requestlog.OwnerDestination {
		need = gen.DestinationsRead
	}
	if !id.Can(auth.Permission(need)) {
		return nil, forbidden(need)
	}
	sum := requestRecordSummaryOf(rec.Summary)
	out := gen.RequestRecord{
		Id: sum.Id, At: sum.At, Direction: sum.Direction, Operation: sum.Operation, Method: sum.Method, Url: sum.Url,
		Status: sum.Status, LatencyMs: sum.LatencyMs, Via: sum.Via, Attempt: sum.Attempt, ErrorClass: sum.ErrorClass,
		ErrorCode: sum.ErrorCode, Check: sum.Check, CheckRun: sum.CheckRun, ConnectionId: sum.ConnectionId,
		Destination: sum.Destination, AlertGroup: sum.AlertGroup, RequestBodyTruncated: sum.RequestBodyTruncated,
		ResponseBodyTruncated: sum.ResponseBodyTruncated,
		RequestHeaders:        headersOf(rec.RequestHeaders), RequestBody: rec.RequestBody,
		RequestBodyBytes: int(rec.RequestBodyBytes), ResponseBody: nullableString(rec.ResponseBody),
		ResponseBodyBytes: int(rec.ResponseBodyBytes), Error: nullableString(rec.Error),
	}
	if rec.ResponseHeaders != nil {
		out.ResponseHeaders.Set(gen.NullableHttpHeaders(rec.ResponseHeaders))
	} else {
		out.ResponseHeaders.SetNull()
	}
	return gen.GetRequestRecord200JSONResponse(out), nil
}

// requestRecordSummaryOf is the API form of a record as a list shows it.
func requestRecordSummaryOf(r requestlog.Summary) gen.RequestRecordSummary {
	out := gen.RequestRecordSummary{
		Id: r.PublicID, At: r.At.UTC(), Direction: gen.RequestDirection(r.Direction), Operation: r.Operation,
		Method: r.Method, Url: r.URL, LatencyMs: int(r.LatencyMS), Via: gen.ConnectionPath(r.Via), Attempt: r.Attempt,
		ErrorClass:           nullableEnum[gen.NullableRequestErrorClass](r.ErrorClass),
		ErrorCode:            nullableEnum[gen.NullableRequestErrorCode](r.ErrorCode),
		Check:                nullableEnum[gen.NullableCheckKind](r.CheckKind),
		CheckRun:             nullableString(r.CheckRun),
		ConnectionId:         nullableString(r.Connection),
		RequestBodyTruncated: r.RequestBodyTruncated, ResponseBodyTruncated: r.ResponseBodyTruncated,
	}
	if r.Status != nil {
		out.Status.Set(*r.Status)
	} else {
		out.Status.SetNull()
	}
	if d := r.Destination; d != nil {
		out.Destination.Set(gen.DestinationRef{Id: d.PublicID, Name: d.Name, Type: gen.DestinationType(d.Type),
			Health: healthOf(destinations.Health{State: d.Health, Since: d.BrokenSince, Reason: d.BrokenReason})})
	} else {
		out.Destination.SetNull()
	}
	if g := r.AlertGroup; g != nil {
		out.AlertGroup.Set(gen.AlertGroupRef{Id: g.PublicID, Number: int(g.Number)})
	} else {
		out.AlertGroup.SetNull()
	}
	return out
}

// nullableEnum is v as a nullable enum of the API, null for nil.
func nullableEnum[T ~string](v *string) nullable.Nullable[T] {
	var out nullable.Nullable[T]
	if v == nil {
		out.SetNull()
	} else {
		out.Set(T(*v))
	}
	return out
}

// headersOf is the API form of headers, never nil.
func headersOf(h map[string][]string) gen.HttpHeaders {
	if h == nil {
		return gen.HttpHeaders{}
	}
	return gen.HttpHeaders(h)
}
