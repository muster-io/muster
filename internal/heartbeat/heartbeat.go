// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package heartbeat holds the Heartbeat of C-07: the endpoint on the ingest listener that records the signals of an
// Integration's Alertmanager, the four Heartbeat states, the liveness clock that the signals advance, and the
// Leader's Heartbeat check that makes a silent Integration Heartbeat lost and raises MusterHeartbeatLost. The Heartbeat
// settings themselves are part of the Integration (internal/integrations); the Stale scan, which counts only the time
// the liveness clock covers, is processing's (internal/ingest).
package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/heartbeat/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
)

// The Heartbeat states of an Integration (C-07.FR-3).
const (
	StateNotConfigured = integrations.HeartbeatNotConfigured
	StateWaiting       = "waiting"
	StateLive          = "live"
	StateLost          = "lost"
)

// Queries are the queries of the package, with those that raise and resolve Internal alerts and the live hint.
type Queries interface {
	LockHeartbeat(ctx context.Context, arg dbgen.LockHeartbeatParams) (dbgen.LockHeartbeatRow, error)
	RecordSignal(ctx context.Context, arg dbgen.RecordSignalParams) error
	MarkLost(ctx context.Context, arg dbgen.MarkLostParams) error
	ListOverdue(ctx context.Context, arg dbgen.ListOverdueParams) ([]int64, error)
	ListHeartbeats(ctx context.Context, orgID int64) ([]dbgen.ListHeartbeatsRow, error)
	GetLeaderStart(ctx context.Context) (dbgen.GetLeaderStartRow, error)
	internalalerts.Store
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	internalalerts.Store
	exec db.Execer
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: internalalerts.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

// Config is what a Service needs.
type Config struct {
	OrgID int64
	Store Store
	// Business is the business clock: the times of the signals and the timeouts follow it, and so the development
	// clock.
	Business clock.Clock
	Log      *logging.Logger
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterHeartbeatLost.
	RunbookBase string
}

// Service records the Heartbeat signals of the Integrations of an Organization.
type Service struct {
	orgID    int64
	store    Store
	clock    clock.Clock
	log      *logging.Logger
	internal *internalalerts.Raiser
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	return &Service{orgID: cfg.OrgID, store: cfg.Store, clock: cfg.Business, log: cfg.Log,
		internal: internalalerts.NewRaiser(cfg.OrgID, cfg.RunbookBase)}
}

// step is what one signal at a time does to a Heartbeat: the time it records as the last signal, how far it advances
// the liveness clock, and whether it makes the Heartbeat live — from waiting (first) or from lost (recovered).
type step struct {
	at        time.Time
	advanceMs int64
	first     bool
	recovered bool
}

// signalStep is the step of a signal at now (C-07.FR-3, schema.md 4.7): waiting becomes live without advancing the
// clock; live adds the gap since the last signal when it is at most the timeout, and nothing after a longer gap — a
// loss the check has not seen yet, or Muster's own downtime; lost becomes live without advancing it. The last signal
// never moves back, so that a signal whose transaction committed late leaves the clock alone.
func signalStep(state string, last *time.Time, timeout time.Duration, now time.Time) step {
	s := step{at: now}
	if last != nil && last.After(now) {
		s.at = *last
	}
	switch state {
	case StateWaiting:
		s.first = true
	case StateLost:
		s.recovered = true
	case StateLive:
		if last != nil {
			if gap := now.Sub(*last); gap > 0 && gap <= timeout {
				s.advanceMs = gap.Milliseconds()
			}
		}
	}
	return s
}

// Signal records a Heartbeat signal of the Integration integrationID, which its token named (C-07.FR-1), in one
// short transaction that locks the Integration. An Integration whose Heartbeat is off records nothing, so that turning
// the Heartbeat on later starts from waiting; a deleted one, or the built-in one, records nothing either. A signal that
// makes the Heartbeat live sends the live hint integration and is logged, and one that ends a loss resolves
// MusterHeartbeatLost in the same transaction.
func (s *Service) Signal(ctx context.Context, integrationID int64) error {
	var (
		publicID string
		st       step
		recorded bool
	)
	err := s.store.InTx(ctx, func(q Queries) error {
		recorded = false
		row, err := q.LockHeartbeat(ctx, dbgen.LockHeartbeatParams{OrgID: s.orgID, ID: integrationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock the heartbeat of integration %d: %w", integrationID, err)
		}
		if !row.HeartbeatEnabled || row.Builtin {
			return nil
		}
		publicID = row.PublicID
		// The time is read under the lock, so that signals of one Integration record times in the order they commit.
		now := s.clock.Now().UTC()
		st = signalStep(row.HeartbeatState, timeOf(row.HeartbeatLastSignalAt),
			time.Duration(row.HeartbeatTimeoutSeconds)*time.Second, now)
		if err := q.RecordSignal(ctx, dbgen.RecordSignalParams{OrgID: s.orgID, ID: row.ID, SignalAt: st.at,
			AdvanceMs: st.advanceMs}); err != nil {
			return fmt.Errorf("record the heartbeat signal of %s: %w", row.PublicID, err)
		}
		recorded = true
		if st.recovered {
			if err := s.internal.Resolve(ctx, q, now, internalalerts.HeartbeatLost, row.PublicID); err != nil {
				return fmt.Errorf("resolve MusterHeartbeatLost of %s: %w", row.PublicID, err)
			}
		}
		if st.first || st.recovered {
			return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: integrations.Hint, ID: row.PublicID})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if recorded && (st.first || st.recovered) {
		s.log.Log(ctx, logging.HeartbeatLive, logging.F("integration", publicID), logging.F("first", st.first))
	}
	return nil
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
