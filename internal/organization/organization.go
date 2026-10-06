// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/organization/dbgen"
)

// The Audit log action and resource type of the organization resource.
const (
	ActionUpdated    = "organization.updated"
	ResourceType     = "organization"
	HintOrganization = "organization"
)

// ErrVersionMismatch is an If-Match that names another version of the organization resource.
var ErrVersionMismatch = errors.New("the organization changed since it was read")

// Covers reports whether the policy demands TOTP of an account; local is an account that has a password (C-03.FR-10).
func (p TOTPPolicy) Covers(local bool) bool {
	return p == TOTPEveryone || (p == TOTPLocalUsers && local)
}

// Proxy is a per-client proxy as stored (C-02.FR-22), without its password.
type Proxy struct {
	Enabled  bool    `json:"enabled"`
	Type     string  `json:"type,omitempty"`
	Address  string  `json:"address,omitempty"`
	Username *string `json:"username,omitempty"`
}

// SecretState is what a read shows of a Secret: whether it is set and when it last changed.
type SecretState struct {
	Set       bool
	UpdatedAt *time.Time
}

// OutgoingHeartbeat is the outgoing heartbeat of the Organization as read: its URL and proxy password are Secrets.
type OutgoingHeartbeat struct {
	URL           SecretState
	Proxy         Proxy
	ProxyPassword SecretState
}

// Organization is the organization resource (C-03.FR-20).
type Organization struct {
	PublicID          string
	Name              string
	TimeZone          string
	SeverityLabel     string
	SeverityMapping   []SeverityMapping
	SeverityStyles    []SeverityStyle
	CriticalIsUrgent  bool
	InstanceLabels    []string
	Retention         Retention
	TOTPRequired      TOTPPolicy
	OIDCTokenGrace    time.Duration
	OutgoingHeartbeat OutgoingHeartbeat
	Version           int64
}

// SecretInput is a Secret field of an update: omitted keeps the stored value, null clears it, a value replaces it.
type SecretInput struct {
	Given bool
	Null  bool
	Value string
}

// changes reports whether the input changes a Secret that is set or not.
func (in SecretInput) changes(set bool) bool {
	return in.Given && (!in.Null || set)
}

// ProxyInput is a per-client proxy as written: an omitted type, address or username keeps the stored one and a null
// username clears it.
type ProxyInput struct {
	Enabled     bool
	Type        *string
	Address     *string
	UsernameSet bool
	Username    *string
	Password    SecretInput
}

// Input is an update of the organization resource.
type Input struct {
	Name                  string
	TimeZone              string
	SeverityLabel         string
	SeverityMapping       []SeverityMapping
	SeverityStyles        []SeverityStyle
	CriticalIsUrgent      bool
	InstanceLabels        []string
	Retention             Retention
	TOTPRequired          TOTPPolicy
	OIDCTokenGraceSeconds int64
	HeartbeatURL          SecretInput
	HeartbeatProxy        ProxyInput
}

// UnsupportedError refuses an update that changes fields that are not editable yet (C-03.FR-20): each pointer names
// one of them in the request body.
type UnsupportedError struct {
	Pointers []string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("the fields %v cannot be changed yet", e.Pointers)
}

// SettingsQueries are the queries of the organization resource, with the insert of the Audit log and the hint.
type SettingsQueries interface {
	GetSettings(ctx context.Context, orgID int64) (dbgen.GetSettingsRow, error)
	LockSettings(ctx context.Context, orgID int64) (int64, error)
	SetTOTPPolicy(ctx context.Context, arg dbgen.SetTOTPPolicyParams) (int64, error)
	audit.Store
	// Notify sends a live-update hint, in the transaction when the queries run in one.
	Notify(ctx context.Context, h db.Hint) error
}

// SettingsStore runs the queries alone or in one transaction.
type SettingsStore interface {
	SettingsQueries
	InTx(ctx context.Context, f func(SettingsQueries) error) error
}

// NewSettingsStore is the SettingsStore over the main pool.
func NewSettingsStore(pool *pgxpool.Pool) SettingsStore {
	return pgSettings{pgSettingsQueries: newSettingsQueries(pool), pool: pool}
}

type pgSettingsQueries struct {
	*dbgen.Queries
	audit.Store
	exec db.Execer
}

func newSettingsQueries(d dbgen.DBTX) pgSettingsQueries {
	return pgSettingsQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgSettingsQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgSettings struct {
	pgSettingsQueries
	pool *pgxpool.Pool
}

func (s pgSettings) InTx(ctx context.Context, f func(SettingsQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newSettingsQueries(tx)) })
}

// Service reads and changes the organization resource of the Organization.
type Service struct {
	orgID int64
	store SettingsStore
	audit *audit.Writer
	clock clock.Clock
}

// NewService returns the Service of the Organization orgID; clock is the business clock.
func NewService(orgID int64, s SettingsStore, w *audit.Writer, business clock.Clock) *Service {
	return &Service{orgID: orgID, store: s, audit: w, clock: business}
}

// Get reads the organization resource.
func (s *Service) Get(ctx context.Context) (Organization, error) {
	return s.get(ctx, s.store)
}

func (s *Service) get(ctx context.Context, q SettingsQueries) (Organization, error) {
	row, err := q.GetSettings(ctx, s.orgID)
	if err != nil {
		return Organization{}, fmt.Errorf("read the organization: %w", err)
	}
	o := Organization{
		PublicID: row.PublicID, Name: row.Name, TimeZone: row.TimeZone, SeverityLabel: row.SeverityLabel,
		CriticalIsUrgent: row.CriticalIsUrgent, InstanceLabels: row.InstanceLabels,
		Retention: Retention{
			StoredSnapshotsDays: row.RetentionStoredSnapshotsDays, AlertDetailsDays: row.RetentionAlertDetailsDays,
			AlertGroupSummariesDays: row.RetentionAlertGroupSummariesDays, AuditLogDays: row.RetentionAuditLogDays,
		},
		TOTPRequired:   TOTPPolicy(row.TotpRequired),
		OIDCTokenGrace: time.Duration(row.OidcTokenGraceSeconds) * time.Second,
		OutgoingHeartbeat: OutgoingHeartbeat{
			URL:           SecretState{Set: row.OutgoingHeartbeatUrlSet, UpdatedAt: timePtr(row.OutgoingHeartbeatUrlUpdatedAt)},
			ProxyPassword: SecretState{Set: row.OutgoingHeartbeatProxyPasswordSet, UpdatedAt: timePtr(row.OutgoingHeartbeatProxyPasswordUpdatedAt)},
		},
		Version: row.Version,
	}
	if o.InstanceLabels == nil {
		o.InstanceLabels = []string{}
	}
	for _, f := range []struct {
		name string
		raw  []byte
		into any
	}{
		{"severity mapping", row.SeverityMapping, &o.SeverityMapping},
		{"severity styles", row.SeverityStyles, &o.SeverityStyles},
		{"outgoing heartbeat proxy", row.OutgoingHeartbeatProxy, &o.OutgoingHeartbeat.Proxy},
	} {
		if err := json.Unmarshal(f.raw, f.into); err != nil {
			return Organization{}, fmt.Errorf("read the %s: %w", f.name, err)
		}
	}
	return o, nil
}

// Update applies an update of the organization resource. In this release only the TOTP policy is editable; an update
// that changes any other field is an *UnsupportedError naming each such field, and changes nothing. version is the
// version of If-Match, nil for any; another one is ErrVersionMismatch. A change is recorded in the Audit log as
// organization.updated with its before/after diff and announced to the live-updates streams with an organization
// hint, in the transaction of the change. An update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, r audit.Actor, t audit.Transport, addr netip.Addr, version *int64,
	in Input) (Organization, error) {
	var out Organization
	err := s.store.InTx(ctx, func(q SettingsQueries) error {
		current, err := q.LockSettings(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("lock the organization: %w", err)
		}
		if version != nil && *version != current {
			return ErrVersionMismatch
		}
		before, err := s.get(ctx, q)
		if err != nil {
			return err
		}
		if p := unsupported(before, in); len(p) > 0 {
			return &UnsupportedError{Pointers: p}
		}
		if in.TOTPRequired == before.TOTPRequired {
			out = before
			return nil
		}
		if _, err := q.SetTOTPPolicy(ctx, dbgen.SetTOTPPolicyParams{
			OrgID: s.orgID, TotpRequired: string(in.TOTPRequired), Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("change the TOTP policy: %w", err)
		}
		if out, err = s.get(ctx, q); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r, Transport: t, Action: ActionUpdated,
			Resource: audit.Resource{Type: ResourceType, PublicID: out.PublicID, Name: out.Name},
			Diff:     audit.Diff(viewOf(before), viewOf(out)), SourceAddress: addr,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintOrganization})
	})
	if err != nil {
		return Organization{}, err
	}
	return out, nil
}

// unsupported returns the pointers of the fields that in changes and that are not editable yet.
func unsupported(o Organization, in Input) []string {
	var out []string
	add := func(changed bool, pointer string) {
		if changed {
			out = append(out, pointer)
		}
	}
	add(in.Name != o.Name, "/name")
	add(in.TimeZone != o.TimeZone, "/time_zone")
	add(in.SeverityLabel != o.SeverityLabel, "/severity_label")
	add(!slices.Equal(in.SeverityMapping, o.SeverityMapping), "/severity_mapping")
	add(!slices.Equal(in.SeverityStyles, o.SeverityStyles), "/severity_styles")
	add(in.CriticalIsUrgent != o.CriticalIsUrgent, "/critical_is_urgent")
	add(!slices.Equal(in.InstanceLabels, o.InstanceLabels), "/instance_labels")
	add(in.Retention.StoredSnapshotsDays != o.Retention.StoredSnapshotsDays, "/retention/stored_snapshots_days")
	add(in.Retention.AlertDetailsDays != o.Retention.AlertDetailsDays, "/retention/alert_details_days")
	add(in.Retention.AlertGroupSummariesDays != o.Retention.AlertGroupSummariesDays,
		"/retention/alert_group_summaries_days")
	add(in.Retention.AuditLogDays != o.Retention.AuditLogDays, "/retention/audit_log_days")
	add(time.Duration(in.OIDCTokenGraceSeconds)*time.Second != o.OIDCTokenGrace, "/oidc_token_grace_seconds")
	add(in.HeartbeatURL.changes(o.OutgoingHeartbeat.URL.Set), "/outgoing_heartbeat/url")
	p, stored := in.HeartbeatProxy, o.OutgoingHeartbeat.Proxy
	add(p.Enabled != stored.Enabled, "/outgoing_heartbeat/proxy/enabled")
	add(p.Type != nil && *p.Type != stored.Type, "/outgoing_heartbeat/proxy/type")
	add(p.Address != nil && *p.Address != stored.Address, "/outgoing_heartbeat/proxy/address")
	add(p.UsernameSet && deref(p.Username) != deref(stored.Username), "/outgoing_heartbeat/proxy/username")
	add(p.Password.changes(o.OutgoingHeartbeat.ProxyPassword.Set), "/outgoing_heartbeat/proxy/password")
	return out
}

// view is what the Audit log diff of the organization resource shows.
type view struct {
	Name                  string            `json:"name"`
	TimeZone              string            `json:"time_zone"`
	SeverityLabel         string            `json:"severity_label"`
	SeverityMapping       []SeverityMapping `json:"severity_mapping"`
	SeverityStyles        []SeverityStyle   `json:"severity_styles"`
	CriticalIsUrgent      bool              `json:"critical_is_urgent"`
	InstanceLabels        []string          `json:"instance_labels"`
	TOTPRequired          string            `json:"totp_required"`
	OIDCTokenGraceSeconds int64             `json:"oidc_token_grace_seconds"`
}

func viewOf(o Organization) view {
	return view{
		Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel, SeverityMapping: o.SeverityMapping,
		SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
		TOTPRequired: string(o.TOTPRequired), OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second),
	}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
