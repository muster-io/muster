// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package delivery is the delivery engine (C-11, ADR-0005): the Desired state of each Root message per Alert Group and
// Destination, which the dispatcher's re-render step sets through Enqueue; the delivery worker, which brings the
// actual message to the latest Desired state with one call per change, Urgent first and within the limiters of the
// Destination and its Connection; Thread replies with the Thread batching window; the interactive path for calls a
// person waits for; and the delivery events. Only this package writes deliveries, thread_replies,
// rate_limit_buckets and delivery_events, and only its worker, its Thread replies and its interactive path call the
// messenger adapters (lint 3).
package delivery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/publicid"
)

// Channel is the LISTEN/NOTIFY channel that wakes the delivery workers of every replica when a Desired state changes
// or a Thread reply is queued.
const Channel = "muster_delivery"

// The built-in settings of delivery (defaults.md): delivery.interactive_budget, delivery.thread_alerts_listed,
// delivery.transient_backoff from its first step up to its longest wait, the attempt and time budgets of
// delivery.transient_budget, delivery.broken_probe_interval, delivery.storm_calm_period, the minute over which the new
// Alert Groups of a Route are counted against route.storm_threshold, and the wait of a lost Thread until S-042 gives
// it its rule.
const (
	InteractiveBudget       = 5 * time.Second
	ThreadAlertsListed      = 10
	TransientFirstStep      = 2 * time.Second
	TransientBackoffMax     = 5 * time.Minute
	TransientBudgetAttempts = 10
	TransientBudgetTime     = 30 * time.Minute
	BrokenProbeInterval     = 5 * time.Minute
	StormCalmPeriod         = 5 * time.Minute
	StormWindow             = time.Minute
)

// The defaults of the worker: the lease of a claimed row, renewed before its call, which it outlasts; the rows one
// claim takes, few, so that Urgent work that comes due meanwhile waits behind few calls; the margin after the time its
// tokens are due that a delivery without a token, or after a RetryAfter, waits, so that the interactive path takes the
// next token first; the longest wait between rounds, which bounds what a missed notification costs; the
// shortest one; the first wait after a failed round; and the most Thread replies one delete of their retention
// removes.
const (
	Lease          = time.Minute
	Batch          = 10
	TokenMargin    = 200 * time.Millisecond
	MaxWait        = 30 * time.Second
	MinWait        = 100 * time.Millisecond
	FailureBackoff = time.Second
	RetentionBatch = 5000
)

// The Destination types.
const (
	TypeMattermost = "mattermost"
	TypeTelegram   = "telegram"
	TypeWebhook    = "webhook"
)

// Destination is a Destination as delivery calls it: Connection is the internal id of its Connection, nil for an
// outgoing webhook.
type Destination struct {
	ID         int64
	PublicID   string
	Name       string
	Type       string
	Connection *int64
}

// OutcomeKind classifies what an adapter call ended with (C-11.FR-8).
type OutcomeKind string

// The outcomes of an adapter call; outcomes.go gives each its rule (C-11.FR-8).
const (
	// OutcomeOK is a message the messenger accepted; "not modified" is ok too.
	OutcomeOK         OutcomeKind = "ok"
	OutcomeRetryAfter OutcomeKind = "retry_after"
	OutcomeTransient  OutcomeKind = "transient"
	OutcomeFatal      OutcomeKind = "fatal"
	// OutcomeUnknown is an answer the adapter cannot classify.
	OutcomeUnknown        OutcomeKind = "unknown"
	OutcomeMarkupRejected OutcomeKind = "markup_rejected"
	// OutcomeGone is a Root message that no longer exists in the messenger.
	OutcomeGone OutcomeKind = "gone"
	// OutcomeThreadLost is a Thread the messenger refuses replies to.
	OutcomeThreadLost OutcomeKind = "thread_lost"
)

// Outcomes are every outcome an adapter may answer with.
var Outcomes = []OutcomeKind{OutcomeOK, OutcomeRetryAfter, OutcomeTransient, OutcomeFatal, OutcomeUnknown,
	OutcomeMarkupRejected, OutcomeGone, OutcomeThreadLost}

// Scope is what a RetryAfter holds: the Destination, or every Destination of its Connection.
type Scope string

// The scopes of a RetryAfter.
const (
	ScopeDestination Scope = "destination"
	ScopeConnection  Scope = "connection"
)

// Outcome is what an adapter call ended with. An ok Publication or Thread reply names its message; a RetryAfter the
// exact delay and its scope; a failure carries the provider's error text, masked of secrets and untrusted.
type Outcome struct {
	Kind       OutcomeKind
	MessageID  string
	MessageURL string
	RetryAfter time.Duration
	Scope      Scope
	Error      outbound.Untrusted
}

// Call is one adapter call: its client class (ADR-0015) — delivery for the worker, interactive for the interactive
// path — its Destination, and for a new message its loudness and symbolic Mentions, which the adapter resolves from
// S-037 on. An edit is always Quiet and mentions nobody. Plain asks for the same text without markup, after the
// messenger rejected the markup (C-11.FR-8).
type Call struct {
	Class       outbound.Class
	Destination Destination
	Loudness    groups.Loudness
	Mentions    []groups.Mention
	Plain       bool
}

// Root is the Root message a Thread reply goes under: its message id and, in Telegram, the automatic copy in the
// discussion group and the last reply of a Thread that is not attached.
type Root struct {
	MessageID      string
	ThreadAnchorID string
	ChainLastID    string
}

// Adapter is what a Destination type implements, declared here by its consumer (ADR-0016). Publish sends a new Root
// message, Update edits one, Reply sends into its Thread; LengthLimit is the longest text the messenger takes. Lint 3
// allows these calls only in worker.go, threads.go and interactive.go.
type Adapter interface {
	Publish(ctx context.Context, c Call, m Message) Outcome
	Update(ctx context.Context, c Call, messageID string, m Message) Outcome
	Reply(ctx context.Context, c Call, root Root, m Message) Outcome
	LengthLimit() int
}

// Checker is the Destination check an adapter may offer (C-13.FR-10, C-14.FR-14): whether the bot can still reach and
// write to its chat, without sending a message. The Broken probe uses it from S-035 on.
type Checker interface {
	Check(ctx context.Context, c Call) Outcome
}

// Adapters are the adapters by Destination type, wired in the runtime.
type Adapters map[string]Adapter

// errorClass is the last_error_class of an outcome, empty for those that have none.
func errorClass(k OutcomeKind) string {
	switch k {
	case OutcomeRetryAfter, OutcomeTransient, OutcomeFatal, OutcomeUnknown:
		return string(k)
	case OutcomeOK, OutcomeMarkupRejected, OutcomeGone, OutcomeThreadLost:
	}
	return ""
}

// metricOutcome is the outcome label of muster_delivery_attempts_total, empty for the outcomes the catalogue does not
// count.
func metricOutcome(k OutcomeKind) string {
	switch k {
	case OutcomeOK:
		return "delivered"
	case OutcomeRetryAfter, OutcomeTransient, OutcomeFatal, OutcomeUnknown, OutcomeMarkupRejected:
		return string(k)
	case OutcomeGone, OutcomeThreadLost:
	}
	return ""
}

// queries are the queries of the package.
type queries interface {
	ListRouteDestinations(ctx context.Context, arg dbgen.ListRouteDestinationsParams) (
		[]dbgen.ListRouteDestinationsRow, error)
	GetRouteDelivery(ctx context.Context, arg dbgen.GetRouteDeliveryParams) (dbgen.GetRouteDeliveryRow, error)
	ShareRouteMembership(ctx context.Context, arg dbgen.ShareRouteMembershipParams) error
	EnsureDelivery(ctx context.Context, arg dbgen.EnsureDeliveryParams) (dbgen.EnsureDeliveryRow, error)
	SetDesired(ctx context.Context, arg dbgen.SetDesiredParams) (string, error)
	SetDeliveryUrgent(ctx context.Context, arg dbgen.SetDeliveryUrgentParams) error
	SetThreadBatchUntil(ctx context.Context, arg dbgen.SetThreadBatchUntilParams) error
	InsertThreadReply(ctx context.Context, arg dbgen.InsertThreadReplyParams) error
	CollectAlerts(ctx context.Context, arg dbgen.CollectAlertsParams) (bool, error)
	NotifyDelivery(ctx context.Context, channel string) error
	ClaimDueDeliveries(ctx context.Context, arg dbgen.ClaimDueDeliveriesParams) ([]dbgen.ClaimDueDeliveriesRow, error)
	GetLeasedDelivery(ctx context.Context, arg dbgen.GetLeasedDeliveryParams) (dbgen.GetLeasedDeliveryRow, error)
	RenewDeliveryLease(ctx context.Context, arg dbgen.RenewDeliveryLeaseParams) error
	MarkDelivered(ctx context.Context, arg dbgen.MarkDeliveredParams) error
	RescheduleDelivery(ctx context.Context, arg dbgen.RescheduleDeliveryParams) error
	StartPublication(ctx context.Context, arg dbgen.StartPublicationParams) error
	RecordDelivered(ctx context.Context, arg dbgen.RecordDeliveredParams) (string, error)
	RecordDeliveryRetry(ctx context.Context, arg dbgen.RecordDeliveryRetryParams) (dbgen.RecordDeliveryRetryRow, error)
	EnsureBuckets(ctx context.Context, arg dbgen.EnsureBucketsParams) error
	TakeTokens(ctx context.Context, arg dbgen.TakeTokensParams) (dbgen.TakeTokensRow, error)
	HoldBucket(ctx context.Context, arg dbgen.HoldBucketParams) error
	ClaimDueReplies(ctx context.Context, arg dbgen.ClaimDueRepliesParams) ([]dbgen.ClaimDueRepliesRow, error)
	GetLeasedReply(ctx context.Context, arg dbgen.GetLeasedReplyParams) (dbgen.GetLeasedReplyRow, error)
	RenewReplyLease(ctx context.Context, arg dbgen.RenewReplyLeaseParams) error
	RescheduleReply(ctx context.Context, arg dbgen.RescheduleReplyParams) error
	RecordReplySent(ctx context.Context, arg dbgen.RecordReplySentParams) error
	RecordReplyRetry(ctx context.Context, arg dbgen.RecordReplyRetryParams) (dbgen.RecordReplyRetryRow, error)
	NextDeliveryWork(ctx context.Context, arg dbgen.NextDeliveryWorkParams) (dbgen.NextDeliveryWorkRow, error)
	InsertDeliveryEvent(ctx context.Context, arg dbgen.InsertDeliveryEventParams) error
	GetGroupForDeliveries(ctx context.Context, arg dbgen.GetGroupForDeliveriesParams) (int64, error)
	ListGroupDeliveries(ctx context.Context, arg dbgen.ListGroupDeliveriesParams) ([]dbgen.ListGroupDeliveriesRow, error)
	CountDeliveryQueues(ctx context.Context, arg dbgen.CountDeliveryQueuesParams) ([]dbgen.CountDeliveryQueuesRow,
		error)
	GetRetentionDetailsDays(ctx context.Context, orgID int64) (int64, error)
	DeleteExpiredReplies(ctx context.Context, arg dbgen.DeleteExpiredRepliesParams) (int64, error)
	outcomeQueries
	brokenQueries
	stormQueries
	membershipQueries
	// Internal alerts and live-update hints are written in the transaction of the change they announce.
	internalalerts.Store
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries of the package over the main pool, alone or in short transactions, and over the
// transaction of a change of an Alert Group.
type Store struct {
	begin   db.Beginner
	db      dbgen.DBTX
	queries func(dbgen.DBTX) queries
}

// NewStore is the Store that begins its transactions with b and runs its other queries on d: the main pool for both.
func NewStore(b db.Beginner, d dbgen.DBTX) *Store {
	return &Store{begin: b, db: d, queries: func(d dbgen.DBTX) queries {
		return pgQueries{Queries: dbgen.New(d), Store: internalalerts.NewStore(d), exec: d}
	}}
}

// pgQueries are the queries of the package and of the Internal alerts over one pool or transaction.
type pgQueries struct {
	*dbgen.Queries
	internalalerts.Store
	exec db.Execer
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

// q are the queries over the pool.
func (s *Store) q() queries { return s.queries(s.db) }

// inTx runs f in one short transaction.
func (s *Store) inTx(ctx context.Context, f func(q queries) error) error {
	return pgx.BeginFunc(ctx, s.begin, func(tx pgx.Tx) error { return f(s.queries(tx)) })
}

// inTxWith runs f in one short transaction with the transaction itself, which the renderer reads through.
func (s *Store) inTxWith(ctx context.Context, f func(tx dbgen.DBTX, q queries) error) error {
	return pgx.BeginFunc(ctx, s.begin, func(tx pgx.Tx) error { return f(tx, s.queries(tx)) })
}

// Config is what a Service needs.
type Config struct {
	OrgID int64
	Store *Store
	// Business is the business clock: due times, windows and the time of delivery events.
	Business clock.Clock
	// Renderer renders Root messages and Storm summaries (C-12).
	Renderer Renderer
	Log      *logging.Logger
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterDestinationBroken.
	RunbookBase string
}

// Service is the delivery of an Organization: Enqueue for the dispatcher, the delivery state of Alert Groups, the
// delivery events, the end of a Broken state, the calm check of Storms, the hooks of Routes and Destinations that
// change their Destinations, and the Leader tasks of delivery.
type Service struct {
	orgID    int64
	store    *Store
	clock    clock.Clock
	renderer Renderer
	log      *logging.Logger
	internal *internalalerts.Raiser
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	return &Service{orgID: cfg.OrgID, store: cfg.Store, clock: cfg.Business, renderer: cfg.Renderer, log: cfg.Log,
		internal: internalalerts.NewRaiser(cfg.OrgID, cfg.RunbookBase)}
}

// ErrNotFound is an Alert Group that does not exist in the Organization.
var ErrNotFound = errors.New("no such alert group")

// StateWaitingForBroken is the delivery state the API shows for a pending delivery whose Destination is Broken; it is
// derived, never stored (schema.md §4.11).
const StateWaitingForBroken = "waiting_for_broken_destination"

// State is the delivery state of an Alert Group in one Destination (C-11.FR-16).
type State struct {
	Destination       Ref
	State             string
	ThreadNotAttached bool
	PossibleDuplicate bool
	MessageURL        *string
	Error             *string
	UpdatedAt         time.Time
}

// Ref names a Destination with its health.
type Ref struct {
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
}

// States reads the delivery state of the Alert Group publicID in each of its Destinations, by Destination name.
func (s *Service) States(ctx context.Context, publicID string) ([]State, error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return nil, ErrNotFound
	}
	q := s.store.q()
	gid, err := q.GetGroupForDeliveries(ctx, dbgen.GetGroupForDeliveriesParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read the alert group %s: %w", id, err)
	}
	rows, err := q.ListGroupDeliveries(ctx, dbgen.ListGroupDeliveriesParams{OrgID: s.orgID, AlertGroupID: gid})
	if err != nil {
		return nil, fmt.Errorf("read the deliveries of the alert group %s: %w", id, err)
	}
	out := make([]State, 0, len(rows))
	for _, r := range rows {
		st := State{Destination: Ref{PublicID: r.DestinationPublicID, Name: r.DestinationName,
			Type: r.DestinationType, Health: r.Health, BrokenSince: timeOf(r.BrokenSince),
			BrokenReason: textOf(r.BrokenReason)},
			State: r.State, ThreadNotAttached: r.ThreadState == "unattached", PossibleDuplicate: r.PossibleDuplicate,
			MessageURL: textOf(r.MessageUrl), UpdatedAt: r.UpdatedAt.UTC()}
		if r.State == "not_delivered" {
			st.Error = textOf(r.LastError)
		}
		if r.State == "pending" && r.Health == healthBroken {
			st.State = StateWaitingForBroken
		}
		out = append(out, st)
	}
	return out, nil
}

// queueSeries are the Destinations whose muster_delivery_queue series this replica exports.
var (
	queueMu     sync.Mutex
	queueSeries = map[string]bool{}
)

// ExportQueue sets muster_delivery_queue to the pending deliveries and the due Thread replies of each Destination of
// the Organization that is not deleted, as a Leader task; the series of a Destination that is gone is removed.
// Counting twice sets the same values.
func (s *Service) ExportQueue(ctx context.Context) error {
	rows, err := s.store.q().CountDeliveryQueues(ctx, dbgen.CountDeliveryQueuesParams{OrgID: s.orgID,
		Now: s.clock.Now().UTC()})
	if err != nil {
		return fmt.Errorf("count the delivery queues: %w", err)
	}
	queueMu.Lock()
	defer queueMu.Unlock()
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.PublicID] = true
		queueSeries[r.PublicID] = true
		metrics.DeliveryQueue.With(r.PublicID).Set(float64(r.Queued))
	}
	for id := range queueSeries {
		if !seen[id] {
			metrics.DeliveryQueue.Delete(id)
			delete(queueSeries, id)
		}
	}
	return nil
}

// brokenSeries are the Destinations whose muster_destination_broken series this replica exports.
var (
	brokenMu     sync.Mutex
	brokenSeries = map[string]bool{}
)

// ExportBroken sets muster_destination_broken to 1 for each Broken Destination of the Organization that is not deleted
// and 0 for the healthy ones, as a Leader task; the series of a Destination that is gone is removed. Running it twice
// sets the same values.
func (s *Service) ExportBroken(ctx context.Context) error {
	rows, err := s.store.q().ListDestinationHealth(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("read the health of the destinations: %w", err)
	}
	brokenMu.Lock()
	defer brokenMu.Unlock()
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.PublicID] = true
		brokenSeries[r.PublicID] = true
		v := 0.0
		if r.Health == healthBroken {
			v = 1
		}
		metrics.DestinationBroken.With(r.PublicID).Set(v)
	}
	for id := range brokenSeries {
		if !seen[id] {
			metrics.DestinationBroken.Delete(id)
			delete(brokenSeries, id)
		}
	}
	return nil
}

// PruneReplies deletes, in batches of at most RetentionBatch, the Thread replies of the Organization that are sent,
// dropped or not delivered and were created retention.alert_details before now (schema.md §6), until a batch comes
// back short, and returns how many it deleted. Running it twice deletes nothing more.
func (s *Service) PruneReplies(ctx context.Context, now time.Time) (int64, error) {
	q := s.store.q()
	days, err := q.GetRetentionDetailsDays(ctx, s.orgID)
	if err != nil {
		return 0, fmt.Errorf("read retention.alert_details: %w", err)
	}
	cutoff := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for {
		n, err := q.DeleteExpiredReplies(ctx, dbgen.DeleteExpiredRepliesParams{OrgID: s.orgID, Cutoff: cutoff,
			BatchSize: RetentionBatch})
		total += n
		if err != nil {
			return total, fmt.Errorf("delete the expired thread replies: %w", err)
		}
		if n < RetentionBatch {
			return total, nil
		}
	}
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int8Of(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullInt(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func nonEmpty(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
