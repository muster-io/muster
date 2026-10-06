// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package integrations holds the Integrations of C-05: one per Alertmanager cluster, with its Connection mode, Static
// labels, duplicate window, stored Heartbeat settings and Integration tokens. A token is `mstr_int_` followed by 32
// random bytes; only its SHA-256 is stored and the value is shown once, with the Alertmanager snippet that carries it
// (ADR-0011). Deletion is soft: the Integration leaves every list and its tokens stop working at once, while its
// Stored Snapshots stay until retention. Every change is recorded in the Audit log and announced with the live hint
// integration; the package also exports muster_integration_info. The built-in "Muster" Integration of the Internal
// alerts exists from the first start and cannot be changed, deleted or given a token.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/integrations/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
)

// The Connection modes; L1 has only webhook-only (integration.connection_mode).
const ConnectionWebhookOnly = "webhook_only"

// HeartbeatNotConfigured is the Heartbeat state of an Integration whose Heartbeat is off.
const HeartbeatNotConfigured = "not_configured"

// LongRepeatWarning is processing.long_repeat_warning: a learned repeat interval above it warns about its
// Alertmanager route (C-06.FR-18).
const LongRepeatWarning = time.Hour

// The defaults of the forms and of an omitted Heartbeat timeout: integration.duplicate_window and
// integration.heartbeat_timeout.
const (
	DefaultDuplicateWindow  = 45 * time.Second
	DefaultHeartbeatTimeout = 5 * time.Minute
)

// The Audit log actions and resource types, and the live hint, of Integrations (C-03.FR-14).
const (
	ActionCreated       = "integration.created"
	ActionUpdated       = "integration.updated"
	ActionDeleted       = "integration.deleted"
	ActionTokenCreated  = "integration_token.created"
	ActionTokenRevoked  = "integration_token.revoked"
	ResourceIntegration = "integration"
	ResourceToken       = "integration_token"
	Hint                = "integration"
)

// The paths of the ingestion and Heartbeat endpoints under MUSTER_INGEST_URL.
const (
	IngestPath    = "/api/v1/ingest"
	HeartbeatPath = "/api/v1/heartbeat"
)

const (
	// maxNameLength bounds the name of an Integration or a token, in characters.
	maxNameLength = 200
	// uniqueViolation is the SQLSTATE of a unique index that refused a row, and nameIndex the index of the names of
	// the Integrations that are not deleted.
	uniqueViolation = "23505"
	nameIndex       = "integrations_name_key"
	// demoLock is the advisory lock of the demo start-up step.
	demoLock = 0x6d75737465720018
)

// labelName is the form of a Prometheus label name, which a Static label takes.
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

var (
	// ErrNotFound is an Integration that does not exist in the Organization or is deleted, or a token that is not one
	// of its tokens or is revoked.
	ErrNotFound = errors.New("no such integration or integration token")
	// ErrNameTaken is a name another Integration that is not deleted has.
	ErrNameTaken = errors.New("another integration has this name")
	// ErrVersionMismatch is an If-Match that names another version of the Integration.
	ErrVersionMismatch = errors.New("the integration changed since it was read")
	// ErrBuiltinImmutable is a change, a deletion or a token of the built-in Integration.
	ErrBuiltinImmutable = errors.New("the built-in integration cannot be changed, deleted or given a token")
)

// BuiltinName is the name of the built-in Integration of the Internal alerts (integrations.builtin).
const BuiltinName = "Muster"

// The kinds of the warnings of an Integration (C-06.FR-18).
const (
	WarningSnapshotTruncated  = "snapshot_truncated"
	WarningLongRepeatInterval = "long_repeat_interval"
)

// Warning is a warning of an Integration: snapshot_truncated with the count of truncated groupKeys, or
// long_repeat_interval with the Alertmanager route and its learned repeat interval.
type Warning struct {
	Kind                  string
	TruncatedGroupCount   int64
	RoutePath             string
	RepeatIntervalSeconds int64
}

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// The codes of FieldError.
const (
	CodeInvalidFormat = "invalid_format"
	CodeTooLong       = "too_long"
	CodeOutOfRange    = "out_of_range"
	CodeUnsupported   = "unsupported"
)

// Integration is an Integration as the API reads it.
type Integration struct {
	ID                     int64
	PublicID               string
	Name                   string
	Description            string
	Builtin                bool
	ConnectionMode         string
	StaticLabels           map[string]string
	DuplicateWindowSeconds int64
	Heartbeat              Heartbeat
	SnapshotCount          int64
	// LastSnapshotAt is the receipt time of its newest Stored Snapshot; nil before the first one.
	LastSnapshotAt *time.Time
	// Warnings apply while a groupKey is truncated or a route repeats less often than LongRepeatWarning.
	Warnings  []Warning
	CreatedAt time.Time
	Version   int64
}

// Heartbeat is the stored Heartbeat of an Integration: its settings and its state.
type Heartbeat struct {
	Enabled        bool
	TimeoutSeconds int64
	State          string
	LastSignalAt   *time.Time
	LostSince      *time.Time
}

// Input is what createIntegration and updateIntegration write. A nil Description, or a nil Heartbeat timeout, keeps
// the stored value on an update and takes the default on a creation.
type Input struct {
	Name                   string
	Description            *string
	ConnectionMode         string
	StaticLabels           map[string]string
	DuplicateWindowSeconds int64
	Heartbeat              HeartbeatInput
}

// HeartbeatInput is the Heartbeat settings of an Input.
type HeartbeatInput struct {
	Enabled        bool
	TimeoutSeconds *int64
}

// ListFilter selects a page of Integrations, in the order they were created, after the id After when it is set.
type ListFilter struct {
	After *int64
	Limit int
}

// Page is a page of Integrations; Next, the id to continue after, is nil on the last page.
type Page struct {
	Integrations []Integration
	Next         *int64
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	ListIntegrations(ctx context.Context, arg dbgen.ListIntegrationsParams) ([]dbgen.ListIntegrationsRow, error)
	GetIntegration(ctx context.Context, arg dbgen.GetIntegrationParams) (dbgen.GetIntegrationRow, error)
	LastSnapshotTimes(ctx context.Context, arg dbgen.LastSnapshotTimesParams) ([]dbgen.LastSnapshotTimesRow, error)
	LockIntegration(ctx context.Context, arg dbgen.LockIntegrationParams) (int64, error)
	FindIntegrationByName(ctx context.Context, arg dbgen.FindIntegrationByNameParams) (string, error)
	InsertIntegration(ctx context.Context, arg dbgen.InsertIntegrationParams) (int64, error)
	UpdateIntegration(ctx context.Context, arg dbgen.UpdateIntegrationParams) error
	DeleteIntegration(ctx context.Context, arg dbgen.DeleteIntegrationParams) error
	ListIntegrationInfo(ctx context.Context, orgID int64) ([]dbgen.ListIntegrationInfoRow, error)
	InsertIntegrationToken(ctx context.Context, arg dbgen.InsertIntegrationTokenParams) (int64, error)
	EnsureIntegrationToken(ctx context.Context, arg dbgen.EnsureIntegrationTokenParams) (string, error)
	LockDemo(ctx context.Context, key int64) error
	ListIntegrationTokens(ctx context.Context, arg dbgen.ListIntegrationTokensParams) (
		[]dbgen.ListIntegrationTokensRow, error)
	RevokeIntegrationToken(ctx context.Context, arg dbgen.RevokeIntegrationTokenParams) (
		dbgen.RevokeIntegrationTokenRow, error)
	FindIngestToken(ctx context.Context, arg dbgen.FindIngestTokenParams) (dbgen.FindIngestTokenRow, error)
	TouchIntegrationToken(ctx context.Context, arg dbgen.TouchIntegrationTokenParams) error
	EnsureBuiltin(ctx context.Context, arg dbgen.EnsureBuiltinParams) (string, error)
	CountTruncatedGroupsOf(ctx context.Context, arg dbgen.CountTruncatedGroupsOfParams) (
		[]dbgen.CountTruncatedGroupsOfRow, error)
	ListLongRepeatRoutes(ctx context.Context, arg dbgen.ListLongRepeatRoutesParams) (
		[]dbgen.ListLongRepeatRoutesRow, error)
	audit.Store
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
	audit.Store
	internal internalalerts.Store
	exec     db.Execer
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), internal: internalalerts.NewStore(d), exec: d}
}

func (q pgQueries) FindBuiltinIntegration(ctx context.Context, orgID int64) (int64, error) {
	return q.internal.FindBuiltinIntegration(ctx, orgID)
}

func (q pgQueries) InsertInternalBody(ctx context.Context, arg idb.InsertInternalBodyParams) error {
	return q.internal.InsertInternalBody(ctx, arg)
}

func (q pgQueries) InsertInternalSnapshot(ctx context.Context, arg idb.InsertInternalSnapshotParams) error {
	return q.internal.InsertInternalSnapshot(ctx, arg)
}

func (q pgQueries) NotifyInternalSnapshot(ctx context.Context, arg idb.NotifyInternalSnapshotParams) error {
	return q.internal.NotifyInternalSnapshot(ctx, arg)
}

func (q pgQueries) ListPendingInternalRaises(ctx context.Context, orgID int64) ([][]byte, error) {
	return q.internal.ListPendingInternalRaises(ctx, orgID)
}

func (q pgQueries) ListOpenInternalAlerts(ctx context.Context, arg idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return q.internal.ListOpenInternalAlerts(ctx, arg)
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
	Audit *audit.Writer
	// Business is the business clock, which dates the rows and the use of tokens.
	Business clock.Clock
	// IngestURL is MUSTER_INGEST_URL, the base of the ingestion and Heartbeat URLs.
	IngestURL *url.URL
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of the Internal alerts a rename raises
	// again.
	RunbookBase string
}

// Service holds the Integrations and Integration tokens of the Organization.
type Service struct {
	orgID     int64
	store     Store
	audit     *audit.Writer
	clock     clock.Clock
	ingestURL string
	internal  *internalalerts.Raiser

	// info holds the muster_integration_info series this replica exports, by public_id to name.
	infoMu sync.Mutex
	info   map[string]string
	// infoChanged wakes RunInfo.
	infoChanged chan struct{}

	touches touches
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	base := ""
	if cfg.IngestURL != nil {
		base = strings.TrimSuffix(cfg.IngestURL.String(), "/")
	}
	return &Service{
		orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, clock: cfg.Business, ingestURL: base,
		internal: internalalerts.NewRaiser(cfg.OrgID, cfg.RunbookBase),
		info:     map[string]string{}, infoChanged: make(chan struct{}, 1),
		touches: newTouches(),
	}
}

// IngestURL is the ingestion endpoint: MUSTER_INGEST_URL and /api/v1/ingest.
func (s *Service) IngestURL() string { return s.ingestURL + IngestPath }

// HeartbeatURL is the Heartbeat endpoint without a token: MUSTER_INGEST_URL and /api/v1/heartbeat.
func (s *Service) HeartbeatURL() string { return s.ingestURL + HeartbeatPath }

// List lists the Integrations that are not deleted.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 1000)
	p := dbgen.ListIntegrationsParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if f.After != nil {
		p.AfterID = pgtype.Int8{Int64: *f.After, Valid: true}
	}
	rows, err := s.store.ListIntegrations(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the integrations: %w", err)
	}
	var page Page
	for i, r := range rows {
		if i == limit {
			last := page.Integrations[limit-1].ID
			page.Next = &last
			break
		}
		in, err := integrationOf(dbgen.GetIntegrationRow(r))
		if err != nil {
			return Page{}, err
		}
		page.Integrations = append(page.Integrations, in)
	}
	if err := s.decorate(ctx, s.store, page.Integrations); err != nil {
		return Page{}, err
	}
	return page, nil
}

// Get reads the Integration publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Integration, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (Integration, error) {
	id, err := publicid.Parse(publicid.Integration, publicID)
	if err != nil {
		return Integration{}, ErrNotFound
	}
	r, err := q.GetIntegration(ctx, dbgen.GetIntegrationParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Integration{}, ErrNotFound
	}
	if err != nil {
		return Integration{}, fmt.Errorf("read the integration %s: %w", id, err)
	}
	in, err := integrationOf(r)
	if err != nil {
		return Integration{}, err
	}
	list := []Integration{in}
	if err := s.decorate(ctx, q, list); err != nil {
		return Integration{}, err
	}
	return list[0], nil
}

// decorate sets what every read of an Integration carries besides its row: the receipt time of its newest Stored
// Snapshot and its warnings.
func (s *Service) decorate(ctx context.Context, q Queries, list []Integration) error {
	if err := s.withLastSnapshot(ctx, q, list); err != nil {
		return err
	}
	return s.withWarnings(ctx, q, list)
}

// withWarnings sets the warnings of each Integration (C-06.FR-18): snapshot_truncated while any of its groupKeys is
// truncated, and one long_repeat_interval per Alertmanager route whose learned repeat interval is above
// LongRepeatWarning.
func (s *Service) withWarnings(ctx context.Context, q Queries, list []Integration) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	for i, in := range list {
		ids[i] = in.ID
		list[i].Warnings = []Warning{}
	}
	truncated, err := q.CountTruncatedGroupsOf(ctx, dbgen.CountTruncatedGroupsOfParams{OrgID: s.orgID,
		IntegrationIds: ids})
	if err != nil {
		return fmt.Errorf("count the truncated groupkeys: %w", err)
	}
	for _, r := range truncated {
		if i := slices.Index(ids, r.IntegrationID); i >= 0 {
			list[i].Warnings = append(list[i].Warnings, Warning{Kind: WarningSnapshotTruncated,
				TruncatedGroupCount: r.TruncatedGroupCount})
		}
	}
	routes, err := q.ListLongRepeatRoutes(ctx, dbgen.ListLongRepeatRoutesParams{OrgID: s.orgID, IntegrationIds: ids,
		ThresholdMs: LongRepeatWarning.Milliseconds()})
	if err != nil {
		return fmt.Errorf("list the routes with long repeat intervals: %w", err)
	}
	for _, r := range routes {
		if i := slices.Index(ids, r.IntegrationID); i >= 0 {
			list[i].Warnings = append(list[i].Warnings, Warning{Kind: WarningLongRepeatInterval,
				RoutePath: r.RoutePath, RepeatIntervalSeconds: (r.LearnedRepeatIntervalMs + 500) / 1000})
		}
	}
	return nil
}

// withLastSnapshot sets the receipt time of the newest Stored Snapshot of each Integration.
func (s *Service) withLastSnapshot(ctx context.Context, q Queries, list []Integration) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	for i, in := range list {
		ids[i] = in.ID
	}
	rows, err := q.LastSnapshotTimes(ctx, dbgen.LastSnapshotTimesParams{OrgID: s.orgID, IntegrationIds: ids})
	if err != nil {
		return fmt.Errorf("read the last snapshot times: %w", err)
	}
	for _, r := range rows {
		if i := slices.Index(ids, r.IntegrationID); i >= 0 {
			at := r.ReceivedAt.UTC()
			list[i].LastSnapshotAt = &at
		}
	}
	return nil
}

// lock locks the Integration publicID for a change and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (Integration, error) {
	id, err := publicid.Parse(publicid.Integration, publicID)
	if err != nil {
		return Integration{}, ErrNotFound
	}
	if _, err := q.LockIntegration(ctx, dbgen.LockIntegrationParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Integration{}, ErrNotFound
		}
		return Integration{}, fmt.Errorf("lock the integration %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// check refuses an Input that is not valid; Heartbeats are refused until the Heartbeat endpoint exists.
func check(in Input) error {
	if err := checkName("/name", in.Name); err != nil {
		return err
	}
	if in.ConnectionMode != ConnectionWebhookOnly {
		return &FieldError{Pointer: "/connection_mode", Code: CodeInvalidFormat,
			Detail: "The only Connection mode is webhook_only."}
	}
	for _, name := range slices.Sorted(maps.Keys(in.StaticLabels)) {
		if !labelName.MatchString(name) {
			return &FieldError{Pointer: "/static_labels/" + escapePointer(name), Code: CodeInvalidFormat,
				Detail: "A label name starts with a letter or an underscore and has only letters, digits and " +
					"underscores."}
		}
	}
	if in.DuplicateWindowSeconds < 1 {
		return &FieldError{Pointer: "/duplicate_window_seconds", Code: CodeOutOfRange,
			Detail: "The duplicate window is at least 1 second."}
	}
	if in.Heartbeat.TimeoutSeconds != nil && *in.Heartbeat.TimeoutSeconds < 1 {
		return &FieldError{Pointer: "/heartbeat/timeout_seconds", Code: CodeOutOfRange,
			Detail: "The Heartbeat timeout is at least 1 second."}
	}
	if in.Heartbeat.Enabled {
		return &FieldError{Pointer: "/heartbeat/enabled", Code: CodeUnsupported,
			Detail: "The Heartbeat cannot be switched on yet."}
	}
	return nil
}

// checkName refuses an empty name or one longer than maxNameLength characters, at pointer.
func checkName(pointer, name string) error {
	if strings.TrimSpace(name) == "" {
		return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "The name is empty."}
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	}
	return nil
}

// nameTaken maps the refusal of the unique index of the names to ErrNameTaken.
func nameTaken(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == nameIndex {
		return ErrNameTaken
	}
	return err
}

// resourceOf is an Integration as the Audit log names it.
func resourceOf(in Integration) audit.Resource {
	return audit.Resource{Type: ResourceIntegration, PublicID: in.PublicID, Name: in.Name}
}

// Create creates an Integration with its Heartbeat off; a name another Integration has is ErrNameTaken.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Integration, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := check(in); err != nil {
		return Integration{}, err
	}
	var created Integration
	err := s.store.InTx(ctx, func(q Queries) error {
		var err error
		created, err = s.insert(ctx, q, in)
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionCreated,
			Resource: resourceOf(created), Diff: audit.Created(viewOf(created)), SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: created.PublicID})
	})
	if err != nil {
		return Integration{}, err
	}
	s.exportInfo(created.PublicID, created.Name)
	return created, nil
}

func (s *Service) insert(ctx context.Context, q Queries, in Input) (Integration, error) {
	labels, err := encodeLabels(in.StaticLabels)
	if err != nil {
		return Integration{}, err
	}
	timeout := int64(DefaultHeartbeatTimeout.Seconds())
	if in.Heartbeat.TimeoutSeconds != nil {
		timeout = *in.Heartbeat.TimeoutSeconds
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	id := publicid.New(publicid.Integration)
	if _, err := q.InsertIntegration(ctx, dbgen.InsertIntegrationParams{
		OrgID: s.orgID, PublicID: id, Name: in.Name, Description: description, ConnectionMode: in.ConnectionMode,
		StaticLabels: labels, DuplicateWindowSeconds: in.DuplicateWindowSeconds, HeartbeatTimeoutSeconds: timeout,
		Now: s.clock.Now().UTC(),
	}); err != nil {
		return Integration{}, fmt.Errorf("create the integration: %w", nameTaken(err))
	}
	return s.get(ctx, q, id)
}

// Update replaces the configured fields of the Integration publicID; a non-nil version must be its current one
// (If-Match). The built-in Integration is ErrBuiltinImmutable. A new name raises every open Internal alert about the
// Integration again with it, in the same transaction, so that its name label follows (C-06.AC-11).
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Integration,
	error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := check(in); err != nil {
		return Integration{}, err
	}
	var before, updated Integration
	err := s.store.InTx(ctx, func(q Queries) error {
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if before.Builtin {
			return ErrBuiltinImmutable
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		next := before
		next.Name, next.StaticLabels, next.DuplicateWindowSeconds = in.Name, in.StaticLabels, in.DuplicateWindowSeconds
		if in.Description != nil {
			next.Description = *in.Description
		}
		if in.Heartbeat.TimeoutSeconds != nil {
			next.Heartbeat.TimeoutSeconds = *in.Heartbeat.TimeoutSeconds
		}
		changes := audit.Diff(viewOf(before), viewOf(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		labels, err := encodeLabels(next.StaticLabels)
		if err != nil {
			return err
		}
		if err := q.UpdateIntegration(ctx, dbgen.UpdateIntegrationParams{
			OrgID: s.orgID, ID: before.ID, Name: next.Name, Description: next.Description, StaticLabels: labels,
			DuplicateWindowSeconds: next.DuplicateWindowSeconds, HeartbeatTimeoutSeconds: next.Heartbeat.TimeoutSeconds,
			Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("update the integration %s: %w", before.PublicID, nameTaken(err))
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		if updated.Name != before.Name {
			if err := s.internal.Renamed(ctx, q, s.clock.Now(), internalalerts.EntityIntegration, updated.PublicID,
				updated.Name); err != nil {
				return fmt.Errorf("rename the internal alerts of %s: %w", updated.PublicID, err)
			}
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionUpdated,
			Resource: resourceOf(updated), Diff: changes, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: updated.PublicID})
	})
	if err != nil {
		return Integration{}, err
	}
	if updated.Name != before.Name {
		s.unexportInfo(before.PublicID)
		s.exportInfo(updated.PublicID, updated.Name)
	}
	return updated, nil
}

// Delete deletes the Integration publicID; a non-nil version must be its current one. Its tokens stop working at
// once and it leaves every list; the row and its Stored Snapshots stay. The marker of the deletion goes into its queue
// in the same transaction: processing reaches it after the Stored Snapshots the Integration accepted before and then
// resolves its Alerts and Internal alerts (C-06.FR-16). The built-in Integration is ErrBuiltinImmutable.
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	var before Integration
	err := s.store.InTx(ctx, func(q Queries) error {
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if before.Builtin {
			return ErrBuiltinImmutable
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		now := s.clock.Now().UTC()
		if err := q.DeleteIntegration(ctx, dbgen.DeleteIntegrationParams{OrgID: s.orgID, ID: before.ID,
			Now: now}); err != nil {
			return fmt.Errorf("delete the integration %s: %w", before.PublicID, err)
		}
		if err := s.internal.MarkDeleted(ctx, q, now, before.ID, before.PublicID, before.Name); err != nil {
			return fmt.Errorf("mark the deletion of the integration %s: %w", before.PublicID, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionDeleted,
			Resource: resourceOf(before), Diff: []audit.Change{{Pointer: "/deleted_at", After: now}},
			SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: before.PublicID})
	})
	if err != nil {
		return err
	}
	s.unexportInfo(before.PublicID)
	return nil
}

// EnsureBuiltin creates the built-in "Muster" Integration of the Internal alerts once per Organization
// (C-06.FR-14), as a start-up ensure step: webhook-only, without Static labels and with the Heartbeat off. It changes
// nothing when the Integration exists; an Integration that is not deleted and already has its name stops the start
// with ErrNameTaken.
func EnsureBuiltin(ctx context.Context, q Queries, orgID int64, now time.Time) error {
	_, err := q.EnsureBuiltin(ctx, dbgen.EnsureBuiltinParams{OrgID: orgID,
		PublicID: publicid.New(publicid.Integration), Name: BuiltinName,
		Description:             "Internal alerts: problems Muster reports about itself through its own pipeline.",
		DuplicateWindowSeconds:  int64(DefaultDuplicateWindow.Seconds()),
		HeartbeatTimeoutSeconds: int64(DefaultHeartbeatTimeout.Seconds()), Now: now.UTC()})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("create the built-in integration %s: %w", BuiltinName, nameTaken(err))
	}
	return nil
}

// Demo is the Integration of the development mode: its name and Static labels, and the published token that the
// fake Alertmanager sends with.
type Demo struct {
	Name         string
	StaticLabels map[string]string
	Token        string
	TokenName    string
}

// EnsureDemo creates the demo Integration when no Integration that is not deleted has its name, and stores its token
// when it is missing, revoked or held by another Integration. Muster is the actor of what it writes.
func (s *Service) EnsureDemo(ctx context.Context, d Demo) error {
	if !wellFormed(d.Token) {
		return errors.New("the demo token is not an integration token")
	}
	by := Requester{Actor: audit.System, Transport: audit.TransportSystem}
	return s.store.InTx(ctx, func(q Queries) error {
		if err := q.LockDemo(ctx, demoLock); err != nil {
			return fmt.Errorf("lock the demo integration: %w", err)
		}
		var in Integration
		id, err := q.FindIntegrationByName(ctx, dbgen.FindIntegrationByNameParams{OrgID: s.orgID, Name: d.Name})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			window := int64(DefaultDuplicateWindow.Seconds())
			if in, err = s.insert(ctx, q, Input{Name: d.Name, ConnectionMode: ConnectionWebhookOnly,
				StaticLabels: d.StaticLabels, DuplicateWindowSeconds: window}); err != nil {
				return err
			}
			if err := s.audit.Record(ctx, q, audit.Entry{
				OrgID: s.orgID, Actor: by.Actor, Transport: by.Transport, Action: ActionCreated,
				Resource: resourceOf(in), Diff: audit.Created(viewOf(in)),
				Details: map[string]any{"development_demo": true},
			}); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("find the demo integration: %w", err)
		default:
			if in, err = s.get(ctx, q, id); err != nil {
				return err
			}
		}
		tokenID, err := q.EnsureIntegrationToken(ctx, dbgen.EnsureIntegrationTokenParams{
			OrgID: s.orgID, PublicID: publicid.New(publicid.IntegrationToken), IntegrationID: in.ID,
			Name: text(d.TokenName), TokenHash: hash(d.Token), Now: s.clock.Now().UTC(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store the demo integration token: %w", err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: by.Actor, Transport: by.Transport, Action: ActionTokenCreated,
			Resource: audit.Resource{Type: ResourceToken, PublicID: tokenID, Name: d.TokenName},
			Details:  map[string]any{"integration": in.PublicID, "development_demo": true},
		})
	})
}

// view is an Integration as its Audit log diff shows it.
type view struct {
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	ConnectionMode         string            `json:"connection_mode"`
	StaticLabels           map[string]string `json:"static_labels"`
	DuplicateWindowSeconds int64             `json:"duplicate_window_seconds"`
	Heartbeat              heartbeatView     `json:"heartbeat"`
}

type heartbeatView struct {
	Enabled        bool  `json:"enabled"`
	TimeoutSeconds int64 `json:"timeout_seconds"`
}

func viewOf(in Integration) view {
	labels := in.StaticLabels
	if labels == nil {
		labels = map[string]string{}
	}
	return view{Name: in.Name, Description: in.Description, ConnectionMode: in.ConnectionMode, StaticLabels: labels,
		DuplicateWindowSeconds: in.DuplicateWindowSeconds,
		Heartbeat:              heartbeatView{Enabled: in.Heartbeat.Enabled, TimeoutSeconds: in.Heartbeat.TimeoutSeconds}}
}

func integrationOf(r dbgen.GetIntegrationRow) (Integration, error) {
	labels, err := decodeLabels(r.StaticLabels)
	if err != nil {
		return Integration{}, fmt.Errorf("read the static labels of %s: %w", r.PublicID, err)
	}
	return Integration{
		ID: r.ID, PublicID: r.PublicID, Name: r.Name, Description: r.Description, Builtin: r.Builtin,
		ConnectionMode: r.ConnectionMode, StaticLabels: labels, DuplicateWindowSeconds: r.DuplicateWindowSeconds,
		Heartbeat: Heartbeat{Enabled: r.HeartbeatEnabled, TimeoutSeconds: r.HeartbeatTimeoutSeconds,
			State: r.HeartbeatState, LastSignalAt: timeOf(r.HeartbeatLastSignalAt), LostSince: timeOf(r.HeartbeatLostSince)},
		SnapshotCount: r.SnapshotCount, CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
	}, nil
}

// exportInfo sets the muster_integration_info series of an Integration.
func (s *Service) exportInfo(publicID, name string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok && old != name {
		metrics.IntegrationInfo.Delete(publicID, old)
	}
	s.info[publicID] = name
	metrics.IntegrationInfo.With(publicID, name).Set(1)
}

// unexportInfo removes the muster_integration_info series of an Integration.
func (s *Service) unexportInfo(publicID string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok {
		metrics.IntegrationInfo.Delete(publicID, old)
		delete(s.info, publicID)
	}
}

// RefreshInfo makes muster_integration_info list exactly the Integrations that are not deleted, as the database has
// them, whichever replica changed them.
func (s *Service) RefreshInfo(ctx context.Context) error {
	// The read happens under the lock, so that a local change exported meanwhile is never undone by an older read.
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	rows, err := s.store.ListIntegrationInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the integrations for their info metric: %w", err)
	}
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
	}
	for id, name := range s.info {
		if want[id] != name {
			metrics.IntegrationInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.IntegrationInfo.With(id, name).Set(1)
	}
	return nil
}

// InfoChanged asks RunInfo to refresh muster_integration_info: a hint said an Integration changed, maybe on another
// replica. It never blocks.
func (s *Service) InfoChanged() {
	select {
	case s.infoChanged <- struct{}{}:
	default:
	}
}

// RunInfo keeps muster_integration_info current until ctx ends: it refreshes it at once, after InfoChanged and at
// every tick, which also repairs a refresh that failed.
func (s *Service) RunInfo(ctx context.Context, ticks <-chan time.Time) {
	for {
		_ = s.RefreshInfo(ctx) // a failed refresh is repeated at the next tick
		select {
		case <-ctx.Done():
			return
		case <-s.infoChanged:
		case <-ticks:
		}
	}
}

func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
