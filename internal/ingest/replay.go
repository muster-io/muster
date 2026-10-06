// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
)

// ActionReplayed is the Audit log action of muster ingest replay.
const ActionReplayed = "ingest.replayed"

// ErrUnknownIntegration is a --integration that names no Integration that is not deleted.
var ErrUnknownIntegration = errors.New("no integration has this name")

// ReplayQueries are the queries of a replay, with the insert of the Audit log.
type ReplayQueries interface {
	GetRetention(ctx context.Context, orgID int64) (int64, error)
	FindReplayIntegration(ctx context.Context, arg dbgen.FindReplayIntegrationParams) (
		dbgen.FindReplayIntegrationRow, error)
	ReplaySnapshots(ctx context.Context, arg dbgen.ReplaySnapshotsParams) ([]int64, error)
	NotifySnapshot(ctx context.Context, arg dbgen.NotifySnapshotParams) error
	audit.Store
}

// ReplayStore runs a replay in one transaction.
type ReplayStore interface {
	InTx(ctx context.Context, f func(ReplayQueries) error) error
}

// NewReplayStore is the ReplayStore over the main pool.
func NewReplayStore(pool *pgxpool.Pool) ReplayStore {
	return pgReplayStore{pool: pool}
}

type pgReplayStore struct {
	pool *pgxpool.Pool
}

type replayQueries struct {
	*dbgen.Queries
	audit.Store
}

func (s pgReplayStore) InTx(ctx context.Context, f func(ReplayQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(replayQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx)})
	})
}

// Replay is what muster ingest replay asks for: the period back from now, as given and as a duration, the name of
// one Integration or empty for all, and the --actor name.
type Replay struct {
	Since       time.Duration
	SinceText   string
	Integration string
	Actor       string
}

// Replayed is what a replay did: how many Stored Snapshots it set back to pending, and the Integration when it was
// limited to one.
type Replayed struct {
	Count       int
	Integration *Ref
}

// Replayer replays the Stored Snapshots of an Organization (C-06.FR-17).
type Replayer struct {
	OrgID int64
	Store ReplayStore
	Audit *audit.Writer
	Log   *logging.Logger
	// Business is the business clock: the period counts back from its now.
	Business clock.Clock
}

// Replay sets the Stored Snapshots received within the period and within retention.stored_snapshots, of one
// Integration or of all, back to pending in one transaction: it clears processed_at and processing_error, marks them
// replayed so that processing does not count them on their Integration again, wakes the workers and records
// ingest.replayed in the Audit log. The workers then process them in arrival order as late Snapshots, so those already
// processed change nothing and one that failed is processed with the current code.
func (r *Replayer) Replay(ctx context.Context, req Replay) (Replayed, error) {
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		return Replayed{}, errors.New("the actor is required")
	}
	if req.Since <= 0 {
		return Replayed{}, errors.New("the period is not positive")
	}
	now := r.Business.Now().UTC()
	var out Replayed
	err := r.Store.InTx(ctx, func(q ReplayQueries) error {
		out = Replayed{}
		p := dbgen.ReplaySnapshotsParams{OrgID: r.OrgID, ReplayedAt: now}
		resource := audit.Resource{Type: integrations.ResourceIntegration}
		if req.Integration != "" {
			in, err := q.FindReplayIntegration(ctx, dbgen.FindReplayIntegrationParams{OrgID: r.OrgID,
				Name: req.Integration})
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrUnknownIntegration, req.Integration)
			}
			if err != nil {
				return fmt.Errorf("find the integration %s: %w", req.Integration, err)
			}
			p.IntegrationID = pgtype.Int8{Int64: in.ID, Valid: true}
			out.Integration = &Ref{PublicID: in.PublicID, Name: in.Name}
			resource.PublicID, resource.Name = in.PublicID, in.Name
		}
		days, err := q.GetRetention(ctx, r.OrgID)
		if err != nil {
			return fmt.Errorf("read the retention of stored snapshots: %w", err)
		}
		p.Since = maxTime(now.Add(-req.Since), now.Add(-time.Duration(days)*24*time.Hour))
		ids, err := q.ReplaySnapshots(ctx, p)
		if err != nil {
			return fmt.Errorf("set the stored snapshots back to pending: %w", err)
		}
		out.Count = len(ids)
		slices.Sort(ids)
		for _, id := range slices.Compact(ids) {
			payload, err := json.Marshal(Notification{OrgID: r.OrgID, IntegrationID: id})
			if err != nil {
				return fmt.Errorf("encode the snapshot notification: %w", err)
			}
			if err := q.NotifySnapshot(ctx, dbgen.NotifySnapshotParams{Channel: SnapshotChannel,
				Payload: string(payload)}); err != nil {
				return fmt.Errorf("notify the processing workers: %w", err)
			}
		}
		details := map[string]any{"since": req.SinceText, "integration": nil, "count": out.Count}
		if out.Integration != nil {
			details["integration"] = out.Integration.PublicID
		}
		if resource.PublicID == "" {
			resource = audit.Resource{}
		}
		return r.Audit.Record(ctx, q, audit.Entry{OrgID: r.OrgID, Actor: audit.CLI(actor),
			Transport: audit.TransportCLI, Action: ActionReplayed, Resource: resource, Details: details})
	})
	if err != nil {
		return Replayed{}, err
	}
	integration := ""
	if out.Integration != nil {
		integration = out.Integration.PublicID
	}
	r.Log.Log(ctx, logging.IngestReplayed, logging.F("actor", actor), logging.F("since", req.SinceText),
		logging.F("integration", integration), logging.F("count", out.Count))
	return out, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
