// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package ingest is the ingestion of C-05 on the ingest listener: it takes a webhook with an Integration token,
// stores its body exactly as received as a pending Stored Snapshot in one short insert-only transaction and answers
// 202 once it committed (ADR-0002). It reads the Stored Snapshots for the API. Processing them is C-06.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// SnapshotChannel is the LISTEN/NOTIFY channel that wakes the processing workers when a Stored Snapshot was stored;
// the payload is a Notification.
const SnapshotChannel = "muster_snapshots"

// BodyLimit is ingest.body_limit: the largest body ingestion stores.
const BodyLimit = 16 << 20

// The processing states of a Stored Snapshot.
const (
	StatePending   = "pending"
	StateProcessed = "processed"
	StateFailed    = "failed"
)

// Notification is the payload of a notification on SnapshotChannel: the Integration whose pending Stored Snapshots
// grew.
type Notification struct {
	OrgID         int64 `json:"org_id"`
	IntegrationID int64 `json:"integration_id"`
}

// Queries are the queries of the package.
type Queries interface {
	InsertSnapshotBody(ctx context.Context, arg dbgen.InsertSnapshotBodyParams) error
	InsertStoredSnapshot(ctx context.Context, arg dbgen.InsertStoredSnapshotParams) error
	NotifySnapshot(ctx context.Context, arg dbgen.NotifySnapshotParams) error
	GetRetention(ctx context.Context, orgID int64) (int64, error)
	FindSnapshotIntegration(ctx context.Context, arg dbgen.FindSnapshotIntegrationParams) (
		dbgen.FindSnapshotIntegrationRow, error)
	ListStoredSnapshots(ctx context.Context, arg dbgen.ListStoredSnapshotsParams) ([]dbgen.ListStoredSnapshotsRow, error)
	GetStoredSnapshot(ctx context.Context, arg dbgen.GetStoredSnapshotParams) (dbgen.GetStoredSnapshotRow, error)
	ListPreviewIntegrations(ctx context.Context, orgID int64) ([]dbgen.ListPreviewIntegrationsRow, error)
	ListPreviewBodies(ctx context.Context, arg dbgen.ListPreviewBodiesParams) ([]dbgen.ListPreviewBodiesRow, error)
	ListPreviewSnapshotBodies(ctx context.Context, arg dbgen.ListPreviewSnapshotBodiesParams) (
		[]dbgen.ListPreviewSnapshotBodiesRow, error)
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{Queries: dbgen.New(pool), pool: pool}
}

type pgStore struct {
	*dbgen.Queries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(dbgen.New(tx))
	})
}

// Service stores and reads the Stored Snapshots of the Organization.
type Service struct {
	orgID int64
	store Store
	clock clock.Clock
}

// New returns the Service of the Organization orgID; business is the business clock, which dates the Stored
// Snapshots and decides their retention.
func New(orgID int64, s Store, business clock.Clock) *Service {
	return &Service{orgID: orgID, store: s, clock: business}
}

// Received is a webhook body to store for an Integration, with the request's Content-Type, if any.
type Received struct {
	IntegrationID int64
	Body          []byte
	ContentType   string
}

// Stored is what storing a body made: the public_id of the Stored Snapshot.
type Stored struct {
	PublicID string
}

// Store stores a body as a pending Stored Snapshot in one transaction: the body once per Organization and UTC day,
// the Stored Snapshot received now on the business clock, and the notification that wakes processing, sent at the
// commit. It parses nothing and updates no other row.
func (s *Service) Store(ctx context.Context, r Received) (Stored, error) {
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	day := pgtype.Date{Time: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
	sum := sha256.Sum256(r.Body)
	payload, err := json.Marshal(Notification{OrgID: s.orgID, IntegrationID: r.IntegrationID})
	if err != nil {
		return Stored{}, fmt.Errorf("encode the snapshot notification: %w", err)
	}
	id := publicid.New(publicid.StoredSnapshot)
	err = s.store.InTx(ctx, func(q Queries) error {
		if err := q.InsertSnapshotBody(ctx, dbgen.InsertSnapshotBodyParams{
			OrgID: s.orgID, BodySha256: sum[:], BodyDay: day, Body: r.Body,
		}); err != nil {
			return fmt.Errorf("store the snapshot body: %w", err)
		}
		if err := q.InsertStoredSnapshot(ctx, dbgen.InsertStoredSnapshotParams{
			OrgID: s.orgID, PublicID: id, IntegrationID: r.IntegrationID, ReceivedAt: now, BodyDay: day,
			BodySha256: sum[:], SizeBytes: int64(len(r.Body)),
			ContentType: pgtype.Text{String: r.ContentType, Valid: r.ContentType != ""},
		}); err != nil {
			return fmt.Errorf("store the stored snapshot: %w", err)
		}
		if err := q.NotifySnapshot(ctx, dbgen.NotifySnapshotParams{Channel: SnapshotChannel,
			Payload: string(payload)}); err != nil {
			return fmt.Errorf("notify the processing workers: %w", err)
		}
		return nil
	})
	if err != nil {
		return Stored{}, err
	}
	return Stored{PublicID: id}, nil
}
