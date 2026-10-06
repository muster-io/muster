// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/base64"
	"time"
	"unicode/utf8"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/ingest"
)

// storedSnapshotsCursor names the cursors of listStoredSnapshots.
const storedSnapshotsCursor = "stored-snapshots"

// storedSnapshotKey is the sort key of a listStoredSnapshots cursor: newest first by receipt time, then by id.
type storedSnapshotKey struct {
	ReceivedAt time.Time `json:"t"`
	ID         int64     `json:"i"`
}

// ListStoredSnapshots is listStoredSnapshots: the Stored Snapshots of an Integration, a deleted one included, newest
// first, never older than retention.stored_snapshots.
func (s *Server) ListStoredSnapshots(ctx context.Context, req gen.ListStoredSnapshotsRequestObject) (
	gen.ListStoredSnapshotsResponseObject, error) {
	p := req.Params
	f := ingest.ListFilter{Integration: p.Integration, From: p.From, To: p.To, Limit: pageSize(p.Limit)}
	if p.State != nil {
		for _, st := range *p.State {
			f.States = append(f.States, string(st))
		}
	}
	var key storedSnapshotKey
	if ok, err := decodeCursor(p.Cursor, storedSnapshotsCursor, &key); err != nil {
		return nil, err
	} else if ok {
		f.After = &ingest.Position{ReceivedAt: key.ReceivedAt, ID: key.ID}
	}
	page, err := s.snapshots.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.StoredSnapshotList{Items: make([]gen.StoredSnapshotSummary, 0, len(page.Snapshots))}
	for _, sn := range page.Snapshots {
		out.Items = append(out.Items, storedSnapshotSummaryOf(sn))
	}
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(storedSnapshotsCursor,
			storedSnapshotKey{ReceivedAt: page.Next.ReceivedAt, ID: page.Next.ID}))
	} else {
		out.NextCursor.SetNull()
	}
	return gen.ListStoredSnapshots200JSONResponse(out), nil
}

// GetStoredSnapshot is getStoredSnapshot: the body exactly as received, as text when it is valid UTF-8 and in base64
// otherwise.
func (s *Server) GetStoredSnapshot(ctx context.Context, req gen.GetStoredSnapshotRequestObject) (
	gen.GetStoredSnapshotResponseObject, error) {
	sn, err := s.snapshots.Get(ctx, req.StoredSnapshotId)
	if err != nil {
		return nil, err
	}
	sum := storedSnapshotSummaryOf(sn.Summary)
	out := gen.StoredSnapshot{
		Id: sum.Id, Integration: sum.Integration, ReceivedAt: sum.ReceivedAt, ProcessedAt: sum.ProcessedAt,
		SizeBytes: sum.SizeBytes, State: sum.State, ProcessingError: sum.ProcessingError, GroupKey: sum.GroupKey,
		AlertCount: sum.AlertCount, TruncatedAlerts: sum.TruncatedAlerts, ContentType: nullableString(sn.ContentType),
		Body: string(sn.Body), BodyEncoding: gen.Utf8,
	}
	if !utf8.Valid(sn.Body) {
		out.Body, out.BodyEncoding = base64.StdEncoding.EncodeToString(sn.Body), gen.Base64
	}
	return gen.GetStoredSnapshot200JSONResponse(out), nil
}

func storedSnapshotSummaryOf(sn ingest.Summary) gen.StoredSnapshotSummary {
	return gen.StoredSnapshotSummary{
		Id: sn.PublicID, Integration: gen.EntityRef{Id: sn.Integration.PublicID, Name: sn.Integration.Name},
		ReceivedAt: sn.ReceivedAt.UTC(), ProcessedAt: nullableTime(sn.ProcessedAt), SizeBytes: int(sn.SizeBytes),
		State: gen.SnapshotState(sn.State), ProcessingError: nullableString(sn.ProcessingError),
		GroupKey: nullableString(sn.GroupKey), AlertCount: nullableInt(sn.AlertCount),
		TruncatedAlerts: nullableInt(sn.TruncatedAlerts),
	}
}

func nullableString(v *string) nullable.Nullable[string] {
	var out nullable.Nullable[string]
	if v == nil {
		out.SetNull()
	} else {
		out.Set(*v)
	}
	return out
}

func nullableInt(v *int64) nullable.Nullable[int] {
	var out nullable.Nullable[int]
	if v == nil {
		out.SetNull()
	} else {
		out.Set(int(*v))
	}
	return out
}
