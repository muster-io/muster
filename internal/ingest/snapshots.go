// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// ErrNotFound is a Stored Snapshot that does not exist in the Organization or is older than
// retention.stored_snapshots.
var ErrNotFound = errors.New("no such stored snapshot")

// Ref names an Integration: its public_id and its name.
type Ref struct {
	PublicID string
	Name     string
}

// Summary is a Stored Snapshot as the list shows it.
type Summary struct {
	ID              int64
	PublicID        string
	Integration     Ref
	ReceivedAt      time.Time
	ProcessedAt     *time.Time
	SizeBytes       int64
	State           string
	ProcessingError *string
	GroupKey        *string
	AlertCount      *int64
	TruncatedAlerts *int64
}

// Snapshot is a Stored Snapshot with its body exactly as received and the request's Content-Type, if any.
type Snapshot struct {
	Summary
	Body        []byte
	ContentType *string
}

// Position is the sort key of a Stored Snapshot in the list: newest first by receipt time, then by id.
type Position struct {
	ReceivedAt time.Time
	ID         int64
}

// ListFilter selects a page of the Stored Snapshots of the Integration whose public_id is Integration, received in
// [From, To) and in States when they are set, after the position After when it is set.
type ListFilter struct {
	Integration string
	From, To    *time.Time
	States      []string
	After       *Position
	Limit       int
}

// Page is a page of Stored Snapshots, newest first; Next, the position to continue after, is nil on the last page.
type Page struct {
	Snapshots []Summary
	Next      *Position
}

// notBefore is the oldest receipt time the API shows: retention.stored_snapshots before now on the business clock.
// Partitions are dropped by day, so the API hides what is past the period while its partition still exists.
func (s *Service) notBefore(ctx context.Context) (time.Time, error) {
	days, err := s.store.GetRetention(ctx, s.orgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the retention of stored snapshots: %w", err)
	}
	return s.clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

// List lists the Stored Snapshots of an Integration, a deleted one included, newest first; an unknown Integration has
// none.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	pid, err := publicid.Parse(publicid.Integration, f.Integration)
	if err != nil {
		return Page{}, nil //nolint:nilerr // an id of no Integration has no Stored Snapshots; it is no error
	}
	in, err := s.store.FindSnapshotIntegration(ctx, dbgen.FindSnapshotIntegrationParams{OrgID: s.orgID, PublicID: pid})
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, nil
	}
	if err != nil {
		return Page{}, fmt.Errorf("find the integration %s: %w", pid, err)
	}
	notBefore, err := s.notBefore(ctx)
	if err != nil {
		return Page{}, err
	}
	limit := min(max(f.Limit, 1), 1000)
	p := dbgen.ListStoredSnapshotsParams{OrgID: s.orgID, IntegrationID: in.ID, NotBefore: notBefore,
		From: timestamptz(f.From), To: timestamptz(f.To), States: f.States, PageSize: int32(limit) + 1}
	if p.States == nil {
		p.States = []string{}
	}
	if f.After != nil {
		p.AfterAt = pgtype.Timestamptz{Time: f.After.ReceivedAt, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := s.store.ListStoredSnapshots(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the stored snapshots of %s: %w", in.PublicID, err)
	}
	ref := Ref{PublicID: in.PublicID, Name: in.Name}
	var page Page
	for i, r := range rows {
		if i == limit {
			last := page.Snapshots[limit-1]
			page.Next = &Position{ReceivedAt: last.ReceivedAt, ID: last.ID}
			break
		}
		page.Snapshots = append(page.Snapshots, Summary{
			ID: r.ID, PublicID: r.PublicID, Integration: ref, ReceivedAt: r.ReceivedAt.UTC(),
			ProcessedAt: timeOf(r.ProcessedAt), SizeBytes: r.SizeBytes, State: r.State,
			ProcessingError: textOf(r.ProcessingError), GroupKey: textOf(r.GroupKey), AlertCount: int8Of(r.AlertCount),
			TruncatedAlerts: int8Of(r.TruncatedAlerts),
		})
	}
	return page, nil
}

// Get reads the Stored Snapshot publicID with its body; one older than retention.stored_snapshots is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Snapshot, error) {
	pid, err := publicid.Parse(publicid.StoredSnapshot, publicID)
	if err != nil {
		return Snapshot{}, ErrNotFound
	}
	notBefore, err := s.notBefore(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	day := time.Date(notBefore.Year(), notBefore.Month(), notBefore.Day(), 0, 0, 0, 0, time.UTC)
	r, err := s.store.GetStoredSnapshot(ctx, dbgen.GetStoredSnapshotParams{OrgID: s.orgID, PublicID: pid,
		NotBefore: notBefore, NotBeforeDay: pgtype.Date{Time: day, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the stored snapshot %s: %w", pid, err)
	}
	return Snapshot{
		Summary: Summary{
			ID: r.ID, PublicID: r.PublicID, Integration: Ref{PublicID: r.IntegrationPublicID, Name: r.IntegrationName},
			ReceivedAt: r.ReceivedAt.UTC(), ProcessedAt: timeOf(r.ProcessedAt), SizeBytes: r.SizeBytes, State: r.State,
			ProcessingError: textOf(r.ProcessingError), GroupKey: textOf(r.GroupKey), AlertCount: int8Of(r.AlertCount),
			TruncatedAlerts: int8Of(r.TruncatedAlerts),
		},
		Body: r.Body, ContentType: textOf(r.ContentType),
	}, nil
}

func timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int8Of(n pgtype.Int8) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}
