// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package systemstatus reads the System status (C-19.FR-10): the live replicas with the Leader and their key ids, the
// recovery state, the delivery queues, the Broken Destinations, the Integrations that need attention, the template
// errors, the active Storms and the result of the last outgoing heartbeat. Any replica answers it; it changes nothing.
package systemstatus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/outgoingheartbeat"
	"github.com/muster-io/muster/internal/systemstatus/dbgen"
)

// The issues of an Integration that needs attention.
const (
	IssueHeartbeatLost     = "heartbeat_lost"
	IssueSnapshotTruncated = "snapshot_truncated"
)

// The kinds of the subject of a template error.
const (
	SubjectRoute       = "route"
	SubjectDestination = "destination"
)

// Queries are the reads of the System status; *dbgen.Queries implements them.
type Queries interface {
	GetRuntimeState(ctx context.Context) (dbgen.GetRuntimeStateRow, error)
	LatestDowntime(ctx context.Context) (dbgen.LatestDowntimeRow, error)
	ListDeliveryQueues(ctx context.Context, arg dbgen.ListDeliveryQueuesParams) ([]dbgen.ListDeliveryQueuesRow, error)
	ListBrokenDestinations(ctx context.Context, orgID int64) ([]dbgen.ListBrokenDestinationsRow, error)
	ListIntegrationsAttention(ctx context.Context, orgID int64) ([]dbgen.ListIntegrationsAttentionRow, error)
	ListTemplateErrors(ctx context.Context, orgID int64) ([]dbgen.ListTemplateErrorsRow, error)
	ListActiveStorms(ctx context.Context, orgID int64) ([]dbgen.ListActiveStormsRow, error)
	GetOutgoingHeartbeatResult(ctx context.Context, orgID int64) (dbgen.GetOutgoingHeartbeatResultRow, error)
}

// NewQueries is the Queries over a pool, a connection or a transaction.
func NewQueries(d dbgen.DBTX) Queries {
	return dbgen.New(d)
}

// Config is what the Service reads.
type Config struct {
	OrgID   int64
	Queries Queries
	// Replicas lists the live replicas, those whose record is younger than replica.live_expiry on the real clock.
	Replicas func(ctx context.Context) ([]keyring.Replica, error)
	// Business is the business clock: the recovery window, the alive mark and the due Thread replies follow it.
	Business clock.Clock
	// LeaderAbsence is leader.absence_notice: a Leader whose alive mark is older is not flagged.
	LeaderAbsence time.Duration
	// Version and Commit are those of the binary.
	Version, Commit string
}

// Service reads the System status of the Organization.
type Service struct {
	cfg Config
}

// New returns the Service of cfg.
func New(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// Entity names a configuration entity by its public_id and its current name.
type Entity struct {
	ID   string
	Name string
}

// Replica is a live replica; Leader flags the one that leads, with a fresh alive mark.
type Replica struct {
	ID          string
	Leader      bool
	KeyIDs      []string
	RefreshedAt time.Time
}

// Recovery is the recovery state after downtime: Active while the recovery window lasts, Until its end while it is
// active, and the last downtime, when there was one.
type Recovery struct {
	Active     bool
	PeriodFrom *time.Time
	PeriodTo   *time.Time
	Until      *time.Time
}

// Queue is a Destination with deliveries waiting.
type Queue struct {
	Destination Entity
	Queued      int64
}

// BrokenDestination is a Broken Destination with the masked reason.
type BrokenDestination struct {
	Destination Entity
	Since       time.Time
	Reason      string
}

// IntegrationAttention is an Integration that is Heartbeat lost or has a truncated groupKey.
type IntegrationAttention struct {
	Integration Entity
	Issue       string
	Since       *time.Time
}

// TemplateError is a Route or a Destination whose template keeps failing.
type TemplateError struct {
	SubjectKind string
	Subject     Entity
	Template    string
	Since       time.Time
	Error       string
}

// Storm is an active Storm of a Route.
type Storm struct {
	Route           Entity
	Since           time.Time
	AlertGroupCount int64
}

// OutgoingHeartbeat is the result of the last outgoing heartbeat; without a URL nothing else is set. A failed one has
// the class of its error and the status code of the answer, 0 without one (outgoingheartbeat.FailureOf).
type OutgoingHeartbeat struct {
	Configured     bool
	LastAttemptAt  *time.Time
	LastOK         *bool
	LastError      *string
	LastErrorClass string
	LastStatus     int
}

// Status is the System status.
type Status struct {
	Version               string
	Commit                string
	Replicas              []Replica
	Recovery              Recovery
	DeliveryQueues        []Queue
	BrokenDestinations    []BrokenDestination
	IntegrationsAttention []IntegrationAttention
	TemplateErrors        []TemplateError
	ActiveStorms          []Storm
	OutgoingHeartbeat     OutgoingHeartbeat
}

// Get reads the System status.
func (s *Service) Get(ctx context.Context) (Status, error) {
	q, org := s.cfg.Queries, s.cfg.OrgID
	now := s.cfg.Business.Now()
	out := Status{Version: s.cfg.Version, Commit: s.cfg.Commit}
	runtime, err := q.GetRuntimeState(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Status{}, fmt.Errorf("read the runtime state: %w", err)
	}
	if out.Replicas, err = s.replicas(ctx, runtime, now); err != nil {
		return Status{}, err
	}
	if out.Recovery, err = recovery(ctx, q, runtime, now); err != nil {
		return Status{}, err
	}
	queues, err := q.ListDeliveryQueues(ctx, dbgen.ListDeliveryQueuesParams{OrgID: org, Now: now})
	if err != nil {
		return Status{}, fmt.Errorf("count the delivery queues: %w", err)
	}
	out.DeliveryQueues = make([]Queue, 0, len(queues))
	for _, r := range queues {
		out.DeliveryQueues = append(out.DeliveryQueues, Queue{Destination: Entity{r.PublicID, r.Name}, Queued: r.Queued})
	}
	broken, err := q.ListBrokenDestinations(ctx, org)
	if err != nil {
		return Status{}, fmt.Errorf("list the broken destinations: %w", err)
	}
	out.BrokenDestinations = make([]BrokenDestination, 0, len(broken))
	for _, r := range broken {
		out.BrokenDestinations = append(out.BrokenDestinations, BrokenDestination{
			Destination: Entity{r.PublicID, r.Name}, Since: r.BrokenSince, Reason: r.Reason})
	}
	attention, err := q.ListIntegrationsAttention(ctx, org)
	if err != nil {
		return Status{}, fmt.Errorf("list the integrations that need attention: %w", err)
	}
	out.IntegrationsAttention = make([]IntegrationAttention, 0, len(attention))
	for _, r := range attention {
		out.IntegrationsAttention = append(out.IntegrationsAttention, IntegrationAttention{
			Integration: Entity{r.PublicID, r.Name}, Issue: r.Issue, Since: timePtr(r.Since)})
	}
	templates, err := q.ListTemplateErrors(ctx, org)
	if err != nil {
		return Status{}, fmt.Errorf("list the template errors: %w", err)
	}
	out.TemplateErrors = make([]TemplateError, 0, len(templates))
	for _, r := range templates {
		out.TemplateErrors = append(out.TemplateErrors, TemplateError{SubjectKind: r.Kind,
			Subject: Entity{r.PublicID, r.Name}, Template: r.Template, Since: r.Since, Error: r.Error})
	}
	storms, err := q.ListActiveStorms(ctx, org)
	if err != nil {
		return Status{}, fmt.Errorf("list the active storms: %w", err)
	}
	out.ActiveStorms = make([]Storm, 0, len(storms))
	for _, r := range storms {
		out.ActiveStorms = append(out.ActiveStorms, Storm{Route: Entity{r.PublicID, r.Name}, Since: r.StartedAt,
			AlertGroupCount: r.AlertGroupCount})
	}
	if out.OutgoingHeartbeat, err = outgoingHeartbeat(ctx, q, org); err != nil {
		return Status{}, err
	}
	return out, nil
}

// replicas lists the live replicas and flags the Leader of runtime_state, while its alive mark is younger than
// leader.absence_notice: the id of a Leader that stopped stays in the row until the next takeover.
func (s *Service) replicas(ctx context.Context, runtime dbgen.GetRuntimeStateRow, now time.Time) ([]Replica, error) {
	live, err := s.cfg.Replicas(ctx)
	if err != nil {
		return nil, err
	}
	leading := runtime.LeaderReplicaID.Valid && runtime.AliveAt.Valid &&
		now.Sub(runtime.AliveAt.Time) <= s.cfg.LeaderAbsence
	out := make([]Replica, 0, len(live))
	for _, r := range live {
		keys := r.KeyIDs
		if keys == nil {
			keys = []string{}
		}
		out = append(out, Replica{ID: r.ID, Leader: leading && r.ID == runtime.LeaderReplicaID.String, KeyIDs: keys,
			RefreshedAt: r.RefreshedAt})
	}
	return out, nil
}

// recovery is the recovery window of runtime_state at now and the last downtime.
func recovery(ctx context.Context, q Queries, runtime dbgen.GetRuntimeStateRow, now time.Time) (Recovery, error) {
	var out Recovery
	if runtime.RecoveryUntil.Valid && now.Before(runtime.RecoveryUntil.Time) {
		until := runtime.RecoveryUntil.Time
		out.Active, out.Until = true, &until
	}
	d, err := q.LatestDowntime(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Recovery{}, fmt.Errorf("read the last downtime: %w", err)
	default:
		out.PeriodFrom, out.PeriodTo = &d.StartedAt, &d.EndedAt
	}
	return out, nil
}

func outgoingHeartbeat(ctx context.Context, q Queries, org int64) (OutgoingHeartbeat, error) {
	r, err := q.GetOutgoingHeartbeatResult(ctx, org)
	if err != nil {
		return OutgoingHeartbeat{}, fmt.Errorf("read the outgoing heartbeat: %w", err)
	}
	out := OutgoingHeartbeat{Configured: r.Configured}
	if !r.Configured {
		return out, nil
	}
	out.LastAttemptAt = timePtr(r.OutgoingHeartbeatLastAttemptAt)
	if r.OutgoingHeartbeatLastOk.Valid {
		ok := r.OutgoingHeartbeatLastOk.Bool
		out.LastOK = &ok
	}
	if r.OutgoingHeartbeatLastError.Valid {
		e := r.OutgoingHeartbeatLastError.String
		out.LastError = &e
		f := outgoingheartbeat.FailureOf(e)
		out.LastErrorClass, out.LastStatus = f.Class, f.Status
	}
	return out, nil
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
